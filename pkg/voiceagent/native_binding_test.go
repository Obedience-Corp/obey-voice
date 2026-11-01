package voiceagent

import (
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestNativeBindingRejectsChangedConfigPolicyAndConversationEpoch(t *testing.T) {
	candidate, _, _ := nativeAgentFixture(t)
	cfg := candidate.PreparedConfig()
	if err := candidate.ValidateBinding(cfg, "conversation", "off", false); err != nil {
		t.Fatal(err)
	}
	changed := cfg.Clone()
	changed.ActivePersona = "different-persona"
	if err := candidate.ValidateBinding(changed, "conversation", "off", false); err == nil {
		t.Fatal("different persona adopted prepared resources")
	}
	changed = cfg.Clone()
	changed.OllamaModel = "different-model"
	if err := candidate.ValidateBinding(changed, "conversation", "off", false); err == nil {
		t.Fatal("different model adopted prepared resources")
	}
	if err := candidate.ValidateBinding(cfg, "conversation", "required", false); err == nil {
		t.Fatal("different policy adopted prepared resources")
	}
	scope := work.WorkScope{SessionID: "same-session", RuntimeEpoch: "runtime", ConversationEpoch: "epoch-one"}
	if err := candidate.BindScope(scope); err != nil {
		t.Fatal(err)
	}
	stale := scope
	stale.ConversationEpoch = "epoch-two"
	if err := candidate.ValidateScope(stale); !errors.Is(err, ErrNativeBindingScope) {
		t.Fatal("stale same-session epoch accepted", err)
	}
	if err := candidate.BindScope(stale); !errors.Is(err, ErrNativeBindingScope) {
		t.Fatal("old resources relabeled for a new epoch", err)
	}
	if err := candidate.ValidateScope(scope); err != nil {
		t.Fatal("failed rebind mutated original scope", err)
	}
}
