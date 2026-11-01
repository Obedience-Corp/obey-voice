package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type recordingSink struct {
	samples []float32
	rate    int
	started chan struct{}
	block   bool
	stopped bool
}

func (s *recordingSink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		rate, err := stream.WaitReady(ctx)
		s.rate = rate
		close(started)
		for frame := range stream.Frames() {
			s.samples = append(s.samples, frame...)
		}
		if s.started != nil {
			close(s.started)
		}
		if s.block {
			<-ctx.Done()
			err = ctx.Err()
		}
		done <- audio.PlaybackResult{Err: err}
	}()
	return audio.NewPlayback(started, done), nil
}
func (s *recordingSink) Stop()         { s.stopped = true }
func (*recordingSink) IsPlaying() bool { return false }
func (*recordingSink) Close() error    { return nil }

func TestReplayOriginalRecordingWithoutModelAndRejectStaleIdentity(t *testing.T) {
	owner := chatmedia.NewAudioSession()
	defer owner.Close()
	ref, err := owner.ImportSamples(t.Context(), []float32{0, .25, -.5})
	if err != nil {
		t.Fatal(err)
	}
	b := &nativeInputBrain{turns: []brain.Turn{{Role: "user", Utterance: &utterance.Finalized{ID: "recording", CaptureEpoch: 7, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}}}}
	p := &Pipeline{Brain: b}
	if err := p.BindAudioMedia(owner); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if !p.RecordingAvailable("recording", 7) {
		t.Fatal("original unavailable")
	}
	if err := p.ReplayRecording(t.Context(), "recording", 7, sink); err != nil {
		t.Fatal(err)
	}
	if sink.rate != 16000 || !reflect.DeepEqual(sink.samples, []float32{0, 8191.0 / 32768, -16383.0 / 32768}) {
		t.Fatal(sink.rate, sink.samples)
	}
	for _, id := range []struct {
		id    string
		epoch uint64
	}{{"recording", 8}, {"other", 7}} {
		if err := p.ReplayRecording(t.Context(), id.id, id.epoch, sink); !errors.Is(err, utterance.ErrAudioUnavailable) {
			t.Fatal(err)
		}
	}
	b.turns = nil
	if p.RecordingAvailable("recording", 7) {
		t.Fatal("cleared row remains replayable")
	}
	if err := p.TranscribeRecording(t.Context(), "recording", 7); err == nil {
		t.Fatal("Standard silently transcribed")
	}
}

func TestExplicitTranscriptionWithAutomaticCaptionsOff(t *testing.T) {
	f := newCaptionFixture(t)
	f.pipeline.Native.Transcript = "off"
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := f.responder.History()[0].Utterance
	close(f.release)
	if err := f.pipeline.TranscribeRecording(t.Context(), input.ID, input.CaptureEpoch); err != nil {
		t.Fatal(err)
	}
	if got := f.responder.History()[0].Utterance.Transcript; got.State != utterance.Ready || got.Text != "actual caption" {
		t.Fatal(got)
	}
	if f.saves.Load() == 0 {
		t.Fatal("retry not persisted")
	}
}

func TestReplayCancellationJoinsPlaybackAndPreservesCanonicalHold(t *testing.T) {
	owner := chatmedia.NewAudioSession()
	defer owner.Close()
	ref, err := owner.ImportSamples(t.Context(), []float32{.25})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "cancel", CaptureEpoch: 8, Audio: &ref}
	p := &Pipeline{Brain: &nativeInputBrain{turns: []brain.Turn{{Role: "user", Utterance: input}}}}
	if err := p.BindAudioMedia(owner); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{started: make(chan struct{}), block: true}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.ReplayRecording(ctx, input.ID, input.CaptureEpoch, sink) }()
	<-sink.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !sink.stopped {
		t.Fatal("playback not stopped")
	}
	if _, err := owner.ResolveAudio(t.Context(), ref); err != nil {
		t.Fatal("canonical hold lost", err)
	}
	owner.Release(ref)
	if p.RecordingAvailable(input.ID, input.CaptureEpoch) {
		t.Fatal("replay leaked a hold")
	}
}

func TestExplicitTranscriptionCancellationJoinsBodyAndReleasesOnlyItsHold(t *testing.T) {
	f := newCaptionFixture(t)
	f.pipeline.Native.Transcript = "off"
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := f.responder.History()[0].Utterance
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.pipeline.TranscribeRecording(ctx, input.ID, input.CaptureEpoch) }()
	<-f.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-f.stopped
	if got := f.responder.History()[0].Utterance.Transcript.State; got != utterance.Disabled {
		t.Fatal("canceled retry published", got)
	}
	if _, err := f.media.ResolveAudio(t.Context(), f.ref); err != nil {
		t.Fatal("canonical hold lost", err)
	}
	f.media.Release(f.ref)
	if f.pipeline.RecordingAvailable(input.ID, input.CaptureEpoch) {
		t.Fatal("retry leaked recording hold")
	}
}

func TestExplicitTranscriptionCannotPublishAfterBindingLeaseRefuses(t *testing.T) {
	f := newCaptionFixture(t)
	f.pipeline.Native.Transcript = "off"
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := f.responder.History()[0].Utterance
	expired := errors.New("binding changed")
	f.pipeline.Native.PublishCaption = func(context.Context, func() error) error { return expired }
	close(f.release)
	if err := f.pipeline.TranscribeRecording(t.Context(), input.ID, input.CaptureEpoch); !errors.Is(err, expired) {
		t.Fatal(err)
	}
	if got := f.responder.History()[0].Utterance.Transcript.State; got != utterance.Disabled {
		t.Fatal("stale binding published", got)
	}
}
