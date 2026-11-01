package voiceagent

import (
	"errors"
	"reflect"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

var ErrNativeBindingScope = errors.New("native resources belong to another session or conversation epoch")

// BindScope stamps fresh prepared resources once. An old candidate cannot be
// relabeled for another conversation, even when the durable SessionID matches.
func (n *NativeCandidate) BindScope(scope work.WorkScope) error {
	if n == nil || !scope.Valid() || n.closed.Load() {
		return ErrNativeBindingScope
	}
	n.scopeMu.Lock()
	defer n.scopeMu.Unlock()
	if n.scope.Valid() && n.scope != scope {
		return ErrNativeBindingScope
	}
	n.scope = scope
	return nil
}
func (n *NativeCandidate) ValidateScope(scope work.WorkScope) error {
	if n == nil || !scope.Valid() || n.closed.Load() {
		return ErrNativeBindingScope
	}
	n.scopeMu.Lock()
	defer n.scopeMu.Unlock()
	if !n.scope.Valid() || n.scope != scope {
		return ErrNativeBindingScope
	}
	return nil
}

// PreparedConfig returns a detached factory snapshot. Nil means an embedding
// supplied injectable providers rather than the ordinary native constructor;
// that host's authorization hook owns its config verification.
func (n *NativeCandidate) PreparedConfig() *config.Config {
	if n == nil || n.cfg == nil {
		return nil
	}
	return n.cfg.Clone()
}
func (n *NativeCandidate) ValidateBinding(cfg *config.Config, controls, transcript string, retain bool) error {
	if n == nil || n.cfg == nil || cfg == nil || n.Policy == nil || !reflect.DeepEqual(n.cfg, cfg) || n.Policy.Controls != controls || n.Policy.Transcript != transcript || n.Policy.RetainAudio != retain {
		return errors.New("prepared native resources do not match the resolved persona configuration and policies")
	}
	return nil
}
