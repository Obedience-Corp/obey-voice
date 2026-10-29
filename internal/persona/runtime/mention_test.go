package personaruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func TestMentionSelectsPersonaAndPreservesRequestWithoutClassifier(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("jarvis", "Jarvis", "jarvis"), testProfile("santa", "Santa", "samantha"))
	defer f.close(t)
	f.selector.opts.AddressResolver = addressResolverFunc(func(context.Context, brain.AddressRequest) (brain.Addressee, error) {
		t.Error("explicit mention invoked classifier")
		return brain.Addressee{}, errors.New("offline")
	})
	got, err := f.selector.Route(t.Context(), "@santa Can you help me configure a voice?", false)
	if err != nil || got.Envelope.PersonaID != "santa" || got.Request != "Can you help me configure a voice?" || got.LocalReply != "" {
		t.Fatalf("admission=%+v err=%v", got, err)
	}
	if f.selector.Snapshot().PersonaID != "santa" {
		t.Fatal("mention did not persist selection")
	}
	if _, err = f.selector.Route(t.Context(), "@missing help", false); err == nil {
		t.Fatal("unknown mention was sent to the selected persona")
	}
	if f.selector.Snapshot().PersonaID != "santa" {
		t.Fatal("failed mention changed selection")
	}
}
