//go:build integration

package stt

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The opt-in fixture is generated/recorded independently, never from the mic
// during this test. It runs the installed Parakeet and Silero models.
func TestLiveParakeetSpeechAndSilence(t *testing.T) {
	fixture := os.Getenv("OBEY_VOICE_LIVE_STT_FIXTURE")
	models := os.Getenv("OBEY_VOICE_LIVE_STT_MODELS")
	if fixture == "" || models == "" {
		t.Skip("set OBEY_VOICE_LIVE_STT_FIXTURE and OBEY_VOICE_LIVE_STT_MODELS")
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: config.DefaultParakeetModel, VADThreshold: .6, VADMinSpeechDuration: .1, VADSilenceDuration: .5, VADPreRollMS: 300, ListenTimeout: 10}
	vad, err := audio.NewVAD(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()
	source, err := audio.NewFixtureSourceFromWAV(fixture, audio.ChunkSize, false)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewParakeetSTT(cfg, source, vad)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Delete()
	for _, tc := range []struct {
		name   string
		source audioSource
		want   string
	}{
		{"speech", source, os.Getenv("OBEY_VOICE_LIVE_STT_EXPECT")},
		{"silence", &silenceFixture{remaining: 30}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider.capture = tc.source
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			session, err := provider.Start(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			var transcript string
			for event := range session.Events() {
				switch e := event.(type) {
				case FinalTranscript:
					transcript = e.Text
				case Failure:
					t.Fatal(e.Err)
				}
			}
			t.Logf("transcript=%q", transcript)
			if tc.name == "silence" && transcript != "" {
				t.Fatalf("silence became %q", transcript)
			}
			if tc.name == "speech" && (transcript == "" || !strings.Contains(strings.ToLower(transcript), strings.ToLower(tc.want))) {
				t.Fatalf("transcript=%q want %q", transcript, tc.want)
			}
		})
	}
}

type silenceFixture struct{ remaining int }

func (s *silenceFixture) Read() []float32 {
	if s.remaining <= 0 {
		return nil
	}
	s.remaining--
	return make([]float32, audio.ChunkSize)
}
func (s *silenceFixture) Exhausted() bool { return s.remaining <= 0 }
