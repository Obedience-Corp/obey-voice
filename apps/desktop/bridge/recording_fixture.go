//go:build desktopfixture

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	engine "github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Only the explicit test build replaces hardware playback. It still calls the
// production resolver/player path and records actual sink PCM, never model audio.
func configureRecordingFixture(rt *engine.DesktopConversation) {
	if os.Getenv("OBEY_VOICE_REPLAY_FIXTURE") != "1" {
		return
	}
	rt.ReplayRecording = func(ctx context.Context, id string, epoch uint64) error {
		sink := &fixtureRecordingSink{}
		if err := rt.Pipeline.ReplayRecording(ctx, id, epoch, sink); err != nil {
			return err
		}
		hash := sha256.Sum256(sink.pcm)
		data, err := json.Marshal(map[string]any{"id": id, "epoch": epoch, "samples": len(sink.pcm) / 2, "sample_rate": sink.rate, "pcm_sha256": hex.EncodeToString(hash[:])})
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(os.Getenv("OBEY_VOICE_TEST_CONFIG"), "replay-receipt.json"), data, 0600)
	}
}

type fixtureRecordingSink struct {
	pcm  []byte
	rate int
}

func (s *fixtureRecordingSink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		rate, err := stream.WaitReady(ctx)
		s.rate = rate
		close(started)
		if err == nil {
			for frame := range stream.Frames() {
				for _, sample := range frame {
					var encoded [2]byte
					binary.LittleEndian.PutUint16(encoded[:], uint16(int16(sample*32768)))
					s.pcm = append(s.pcm, encoded[:]...)
				}
			}
			err = stream.Err()
		}
		done <- audio.PlaybackResult{Err: err}
	}()
	return audio.NewPlayback(started, done), nil
}
func (*fixtureRecordingSink) Stop()           {}
func (*fixtureRecordingSink) IsPlaying() bool { return false }
func (*fixtureRecordingSink) Close() error    { return nil }
