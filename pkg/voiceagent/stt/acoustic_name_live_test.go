//go:build integration

package stt

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"os"
	"path/filepath"
	"testing"
)

func TestParakeetAcousticEvidenceNative(t *testing.T) {
	models := os.Getenv("OBEY_KWS_STT_MODELS")
	if models == "" {
		t.Skip("set OBEY_KWS_STT_MODELS to explicit Parakeet/VAD/KWS model root")
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: config.DefaultParakeetModel, VADThreshold: .6, VADMinSpeechDuration: .25, VADSilenceDuration: .5, VADPreRollMS: 300, ListenTimeout: 10, AcousticNameEnabled: true}
	vad, err := audio.NewVAD(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vad.Delete()
	p, err := NewParakeetSTT(cfg, nil, vad)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Delete()
	for _, file := range []string{"positive", "negative"} {
		var baseline string
		for _, enabled := range []bool{false, true} {
			source, err := audio.NewFixtureSourceFromWAV(filepath.Join("../../../testdata/kws", file+".wav"), audio.ChunkSize, false)
			if err != nil {
				t.Fatal(err)
			}
			p.capture = source
			detector := p.acoustic
			if !enabled {
				p.acoustic = nil
			}
			session, err := p.Start(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var final FinalTranscript
			for e := range session.Events() {
				switch e := e.(type) {
				case FinalTranscript:
					final = e
				case Failure:
					t.Fatal(e.Err)
				}
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			p.acoustic = detector
			t.Logf("file=%s enabled=%v transcript=%q keyword=%q", file, enabled, final.Text, final.AcousticName)
			if final.Text == "" {
				t.Fatal("missing transcript")
			}
			if !enabled {
				baseline = final.Text
			} else if baseline != final.Text {
				t.Fatal("acoustic detector changed command")
			}
			want := ""
			if enabled && file == "positive" {
				want = "Obey"
			}
			if final.AcousticName != want {
				t.Fatalf("keyword=%q want=%q", final.AcousticName, want)
			}
		}
	}
}
