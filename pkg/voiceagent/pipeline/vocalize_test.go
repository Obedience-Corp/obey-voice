package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestSplitVocalizations(t *testing.T) {
	tests := []struct {
		in   string
		want []vocalPart
	}{
		{"Hello there.", []vocalPart{{text: "Hello there."}}},
		{"[moan]", []vocalPart{{sound: vocalMoan}}},
		{"[moan] that's it, baby.", []vocalPart{{sound: vocalMoan}, {text: "that's it, baby."}}},
		{"Come here. [gasp] Don't stop.", []vocalPart{{text: "Come here."}, {sound: vocalGasp}, {text: "Don't stop."}}},
		{"[laugh] [moan] more", []vocalPart{{sound: vocalLaugh}, {sound: vocalMoan}, {text: "more"}}},
		{"[AHH] yes", []vocalPart{{sound: vocalMoan}, {text: "yes"}}},
		{"", nil},
	}
	for _, tt := range tests {
		got := splitVocalizations(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitVocalizations(%q) = %#v, want %#v", tt.in, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("splitVocalizations(%q)[%d] = %#v, want %#v", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestStripVocalTags(t *testing.T) {
	if got := stripVocalTags("[moan] that's it, baby."); got != "that's it, baby." {
		t.Errorf("stripVocalTags = %q", got)
	}
	if got := stripVocalTags("[moan]"); got != "" {
		t.Errorf("tag-only strip = %q, want empty", got)
	}
	if got := stripVocalTags("plain speech"); got != "plain speech" {
		t.Errorf("untagged = %q", got)
	}
}

func TestProceduralVocalIsSoundNotSilence(t *testing.T) {
	for _, kind := range []string{vocalMoan, vocalGasp, vocalLaugh, vocalSigh, vocalWhimper, vocalKiss} {
		samples := proceduralVocal(kind, 1)
		if len(samples) < vocalSampleRate/20 {
			t.Errorf("%s: %d samples, want at least 50ms", kind, len(samples))
		}
		var energy float64
		for _, s := range samples {
			energy += float64(s) * float64(s)
		}
		rms := energy / float64(len(samples))
		if rms < 1e-6 {
			t.Errorf("%s: rms %g, want audible energy", kind, rms)
		}
		dur := time.Duration(float64(len(samples)) / vocalSampleRate * float64(time.Second))
		if dur > 2*time.Second {
			t.Errorf("%s: duration %s, want a short sound", kind, dur)
		}
	}
}

func TestVocalBankPickVaries(t *testing.T) {
	b := newVocalBank("")
	first, ok := b.pick(vocalMoan)
	if !ok || len(first.samples) == 0 {
		t.Fatal("expected a procedural moan")
	}
	seen := map[int]bool{len(first.samples): true}
	for i := 0; i < 8; i++ {
		clip, ok := b.pick(vocalMoan)
		if !ok {
			t.Fatal("pick failed")
		}
		seen[len(clip.samples)] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected more than one moan variant, got lengths %v", seen)
	}
}

func TestRunTurnMoanTagDoesNotSendMarkupToTTS(t *testing.T) {
	ttsProvider := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"[moan] that's it, baby."}},
		TTS:    ttsProvider,
		Player: player,
		Events: events.NewBus(),
	}
	p.EnableVocals("")

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	texts := ttsProvider.Texts()
	joined := strings.Join(texts, " ")
	if strings.Contains(strings.ToLower(joined), "moan") {
		t.Errorf("TTS received markup: %v", texts)
	}
	if !strings.Contains(joined, "that's it, baby.") {
		t.Errorf("TTS missed the speech: %v", texts)
	}
}
