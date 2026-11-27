//go:build integration

package stt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/summon"
)

// The whole listening path for a name other than Obey: Silero segments the
// clip, Parakeet transcribes it, the spotter listens to the same samples, and
// a name summon gate judges the result.
//
// With Obey among the names (the default) it checks that listening for more names still
// reports Obey, with a hit time, on the checked-in clip. With OBEY_KWS_CASES
// (a kws_bench manifest) and OBEY_KWS_NAMES it replays those clips too and
// logs transcript, hit and verdict for each; "want_address" in a case, when
// present, is asserted against the gate.
func TestParakeetNamedAcousticEvidenceNative(t *testing.T) {
	models := os.Getenv("OBEY_KWS_STT_MODELS")
	if models == "" {
		t.Skip("set OBEY_KWS_STT_MODELS to explicit Parakeet/VAD/KWS model root")
	}
	names := []string{"Juno", "Obey"}
	if env := os.Getenv("OBEY_KWS_NAMES"); env != "" {
		names = strings.Split(env, ",")
	}
	cfg := &config.Config{ModelsDir: models, ParakeetModel: config.DefaultParakeetModel, VADThreshold: .6, VADMinSpeechDuration: .25, VADSilenceDuration: .5, VADPreRollMS: 300, ListenTimeout: 10, AcousticNameEnabled: true, AcousticNames: names}
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
	hear := func(path string) FinalTranscript {
		t.Helper()
		source, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, false)
		if err != nil {
			t.Fatal(err)
		}
		p.capture = source
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
		return final
	}

	if slices.Contains(names, "Obey") {
		final := hear("../../../testdata/kws/positive.wav")
		t.Logf("positive.wav transcript=%q heard=%q by=%s", final.Text, final.AcousticName, final.AcousticNameBy)
		if final.AcousticName != "Obey" || final.AcousticNameBy <= 0 {
			t.Fatalf("heard=%q by=%s", final.AcousticName, final.AcousticNameBy)
		}
	}

	manifest := os.Getenv("OBEY_KWS_CASES")
	if manifest == "" {
		return
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		File    string `json:"file"`
		Spoken  string `json:"spoken"`
		Address *bool  `json:"want_address"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	transcriptOnly, err := summon.NewGate(names[0], names[1:])
	if err != nil {
		t.Fatal(err)
	}
	withEvidence, err := summon.NewGate(names[0], names[1:], summon.WithAcousticEvidence(summon.DefaultAcousticWindow))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		final := hear(filepath.Join(filepath.Dir(manifest), c.File))
		before := transcriptOnly.Veto(final.Text) == ""
		after := withEvidence.VetoHeard(final.Text, final.AcousticName, final.AcousticNameBy) == ""
		t.Logf("spoken=%q transcript=%q heard=%q by=%.2fs admitted: transcript=%v with-evidence=%v",
			c.Spoken, final.Text, final.AcousticName, final.AcousticNameBy.Seconds(), before, after)
		if c.Address != nil && after != *c.Address {
			t.Errorf("%q: admitted=%v, want %v", c.Spoken, after, *c.Address)
		}
	}
}
