package pipeline

import (
	"context"
	"errors"
	admissionpolicy "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"testing"
)

func TestVoiceAcousticEvidenceDoesNotLeakIntoNextTurnOrText(t *testing.T) {
	stop := errors.New("stop at admission")
	source := &fakeSTT{text: "Oh bay, explain this", acousticName: "Obey"}
	p := &Pipeline{STT: source, Brain: &fakeBrain{}, Events: events.NewBus()}
	want := "Obey"
	p.AdmitInput = func(ctx context.Context, text string, _ bool) (ResponseAdmission, error) {
		if got := admissionpolicy.AcousticName(ctx); got != want {
			t.Errorf("name=%q want=%q", got, want)
		}
		if text != source.text {
			t.Errorf("transcript=%q", text)
		}
		return ResponseAdmission{}, stop
	}
	if text, err := p.RunTurn(t.Context()); text != source.text || !errors.Is(err, stop) {
		t.Fatalf("text=%q err=%v", text, err)
	}
	want = ""
	source.acousticName = ""
	if _, err := p.RunTurn(t.Context()); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(t.Context(), source.text); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestAcousticEvidenceOnlyExistsDuringAdmission(t *testing.T) {
	p := &Pipeline{}
	p.AdmitInput = func(ctx context.Context, text string, background bool) (ResponseAdmission, error) {
		want := "Obey"
		if background {
			want = ""
		}
		if got := admissionpolicy.AcousticName(ctx); got != want {
			t.Fatalf("name=%q want=%q", got, want)
		}
		return ResponseAdmission{Request: text}, nil
	}
	for _, background := range []bool{false, true} {
		ctx, _, err := p.admitInput(t.Context(), "Oh bay", background, &turnMetrics{acousticName: "Obey"})
		if err != nil || admissionpolicy.AcousticName(ctx) != "" {
			t.Fatalf("evidence escaped admission: %v", err)
		}
	}
}
