package pipeline

import (
	"context"
	"errors"
	"testing"

	admissiondiag "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestRunTurnTextModeWrapsAdmissionError(t *testing.T) {
	p := &Pipeline{Brain: &fakeBrain{}, Events: events.NewBus()}
	cause := admissiondiag.WrapFailure(admissiondiag.FailureClassifierUnavailable, "route", errors.New("classifier offline"))
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return ResponseAdmission{}, cause
	}
	err := p.RunTurnTextMode(t.Context(), "hello")
	var wrapped *AdmissionError
	if !errors.As(err, &wrapped) {
		t.Fatalf("error=%v want AdmissionError", err)
	}
	if category, ok := admissiondiag.FailureCategoryOf(err); !ok || category != admissiondiag.FailureClassifierUnavailable {
		t.Fatalf("category=%q ok=%v err=%v", category, ok, err)
	}
}

func TestRunTurnVoiceWrapsAdmissionError(t *testing.T) {
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{},
		Events: events.NewBus(),
	}
	cause := admissiondiag.WrapFailure(admissiondiag.FailureIdentityRefusal, "route", errors.New("persona refused"))
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return ResponseAdmission{}, cause
	}
	text, err := p.RunTurn(t.Context())
	if text != "hello" {
		t.Fatalf("text=%q want recognized transcript", text)
	}
	var wrapped *AdmissionError
	if !errors.As(err, &wrapped) {
		t.Fatalf("error=%v want AdmissionError", err)
	}
	if category, ok := admissiondiag.FailureCategoryOf(err); !ok || category != admissiondiag.FailureIdentityRefusal {
		t.Fatalf("category=%q ok=%v err=%v", category, ok, err)
	}
}
