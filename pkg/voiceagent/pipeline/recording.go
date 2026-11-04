package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/wav"
)

// RecordingActions run at the same serialized boundary as foreground turns,
// persona replacement and Clear. The caller must cancel/join before teardown.
// Identity is the canonical utterance ID plus capture epoch, never a row index.
func (p *Pipeline) recording(id string, epoch uint64) (*utterance.Finalized, *chatmedia.AudioSession, error) {
	if p == nil || p.Brain == nil {
		return nil, nil, utterance.ErrAudioUnavailable
	}
	p.captureMu.Lock()
	owner := p.audioMedia
	p.captureMu.Unlock()
	if owner == nil {
		return nil, nil, utterance.ErrAudioUnavailable
	}
	for _, turn := range p.Brain.History() {
		input := turn.Utterance
		if input != nil && input.ID == id && input.CaptureEpoch == epoch && input.Audio != nil {
			if err := owner.Retain(*input.Audio); err != nil {
				return nil, nil, err
			}
			return input.Clone(), owner, nil
		}
	}
	return nil, nil, utterance.ErrAudioUnavailable
}

// RecordingAvailable inspects only session-owned memory. It never searches
// disk, regenerates audio, calls a model or changes canonical history.
func (p *Pipeline) RecordingAvailable(id string, epoch uint64) bool {
	input, owner, err := p.recording(id, epoch)
	if err != nil {
		return false
	}
	owner.Release(*input.Audio)
	return true
}

// ReplayRecording plays the original admitted PCM at its original sample rate.
// A supplied software engine supports quiet verification; production may open
// an engine only after the user's explicit Replay action.
func (p *Pipeline) ReplayRecording(ctx context.Context, id string, epoch uint64, player audio.Engine) error {
	ctx = audio.BindPlaybackContext(ctx, player)
	if err := ctx.Err(); err != nil {
		return err
	}
	if player == nil {
		return errors.New("recording playback is unavailable")
	}
	input, owner, err := p.recording(id, epoch)
	if err != nil {
		return err
	}
	defer owner.Release(*input.Audio)
	data, err := owner.ResolveAudio(ctx, *input.Audio)
	if err != nil {
		return err
	}
	format, err := wav.ReadWAVFormat(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if format.SampleRate != utterance.SampleRate || format.Channels != 1 || format.BitsPerSample != 16 || format.Frames() != input.Audio.SampleCount {
		return utterance.ErrInvalidAudio
	}
	samples := make([]float32, int(format.Frames()))
	for i := range samples {
		offset := int(format.DataOffset) + i*2
		samples[i] = float32(int16(binary.LittleEndian.Uint16(data[offset:offset+2]))) / 32768
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := audio.NewPCMStream(streamCtx)
	if err := stream.SetSampleRate(format.SampleRate); err != nil {
		return err
	}
	producer := make(chan struct{})
	go func() { defer close(producer); err := stream.Write(samples); stream.CloseWithError(err) }()
	defer func() { cancel(); <-producer }()
	if err := ctx.Err(); err != nil {
		return err
	}
	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}
	if playback == nil {
		return errors.New("recording player returned no completion handle")
	}
	select {
	case <-ctx.Done():
		player.Stop()
		select {
		case <-playback.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return fmt.Errorf("recording playback did not stop: %w", ctx.Err())
		}
	case result := <-playback.Done():
		if result.Interrupted {
			return context.Canceled
		}
		return result.Err
	}
}

// TranscribeRecording is an explicit retry, including when automatic captions
// are off. Only the current Native route can transcribe; Standard never falls
// back to another model/provider. A publication lease fences replacement/Clear.
func (p *Pipeline) TranscribeRecording(ctx context.Context, id string, epoch uint64) error {
	policy := p.Native
	if policy == nil || policy.Client == nil || policy.Scheduler == nil || policy.PublishCaption == nil || policy.retired.Load() {
		return errors.New("select a ready Native persona to transcribe this recording")
	}
	updater, ok := p.Brain.(brain.InputTranscriptUpdater)
	if !ok {
		return errors.New("recording transcript updates are unavailable")
	}
	if err := p.prepareNativeForeground(ctx); err != nil {
		return err
	}
	input, owner, err := p.recording(id, epoch)
	if err != nil {
		return err
	}
	defer owner.Release(*input.Audio)
	var transcript utterance.Transcript
	err = policy.Scheduler.Run(ctx, func(ctx context.Context) error {
		var err error
		transcript, err = policy.Client.Transcribe(ctx, *input.Audio, owner)
		return err
	})
	if err != nil {
		return fmt.Errorf("transcribe recording: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ticket := &nativeCaptionTicket{policy: policy, input: input, updater: updater, pipeline: p}
	return ticket.publish(ctx, transcript)
}
