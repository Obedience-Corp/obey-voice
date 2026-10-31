package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"testing"
)

func TestResponseAdmissionRejectsCapturedInputFromReplacedNativeBinding(t *testing.T) {
	oldPolicy, newPolicy := &NativePolicy{Controls: "conversation"}, &NativePolicy{Controls: "standard"}
	for _, test := range []struct {
		name      string
		old, next *NativePolicy
	}{
		{"standard to native", nil, newPolicy},
		{"native to standard", oldPolicy, nil},
		{"native policy replacement", oldPolicy, newPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &Pipeline{Native: test.old}
			metrics := newTurnMetrics()
			metrics.captureBound = true
			metrics.capturePolicy = test.old
			p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
				p.Native = test.next
				return ResponseAdmission{Request: "old decoded text"}, nil
			}
			_, _, err := p.admitInput(t.Context(), "old decoded text", false, metrics)
			if !errors.Is(err, ErrInputBindingChanged) {
				t.Fatal(err)
			}
			if metrics.originalInput != "" {
				t.Fatal("stale input was bound to new response")
			}
		})
	}
}

func TestResponseAdmissionPreservesSameBindingAndTypedInputs(t *testing.T) {
	policy := &NativePolicy{Controls: "conversation"}
	for _, captured := range []bool{false, true} {
		p := &Pipeline{Native: policy}
		metrics := newTurnMetrics()
		metrics.captureBound = captured
		metrics.capturePolicy = policy
		p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
			if !captured {
				p.Native = &NativePolicy{Controls: "standard"}
			}
			return ResponseAdmission{Request: "request"}, nil
		}
		if _, _, err := p.admitInput(t.Context(), "input", false, metrics); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeConversationAdmissionRejectsReplacedCapturedPolicy(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{.1, .2})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "old-binding-recording", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
	responder := &nativeInputBrain{fakeBrain: fakeBrain{chunks: []string{"must not answer"}}}
	p := &Pipeline{Input: &fixtureNativeInput{input: input}, Native: &NativePolicy{Controls: "conversation", Transcript: "off", Media: media}, Brain: responder}
	p.SetOutputMuted(true)
	p.AdmitAudio = func(context.Context, *utterance.Finalized) (ResponseAdmission, error) {
		p.Native = &NativePolicy{Controls: "conversation", Transcript: "off", Media: media}
		return ResponseAdmission{}, nil
	}
	if _, err := p.RunTurn(t.Context()); !errors.Is(err, ErrInputBindingChanged) {
		t.Fatal(err)
	}
	if responder.opts.Utterance != nil {
		t.Fatal("new binding answered an old recording")
	}
	if _, err := media.ResolveAudio(t.Context(), ref); err == nil {
		t.Fatal("rejected recording reference leaked")
	}
}
