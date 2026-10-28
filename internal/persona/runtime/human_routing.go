package personaruntime

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func (s *Selector) humanSpeakerName(speaker string) string {
	name := strings.TrimSpace(speaker)
	if name != "" && !strings.EqualFold(name, "unknown") {
		return name
	}
	if s.opts.Base != nil {
		if name := strings.TrimSpace(s.opts.Base.UserDisplayName); name != "" {
			return name
		}
	}
	return "Unknown"
}

// Human roles are supplied by the host, not inferred from model output. This
// check is used only after persona lookup fails, so a real named persona still
// follows the roster's ordinary ambiguity/selection rules.
func knownHumanAddressee(req brain.AddressRequest, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" || strings.EqualFold(target, "unknown") {
		return false
	}
	if req.Account == "user" && strings.EqualFold(target, strings.TrimSpace(req.Speaker)) {
		return true
	}
	for _, turn := range req.Conversation {
		if turn.Account == "user" && strings.EqualFold(target, strings.TrimSpace(turn.Name)) {
			return true
		}
	}
	return false
}
