package personaruntime

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

func (s *Selector) humanSpeakerName(speaker string) string {
	if name := brain.PromptSpeaker(speaker); name != "" {
		return name
	}
	if s.opts.Base != nil {
		if name := strings.TrimSpace(s.opts.Base.UserDisplayName); name != "" {
			return name
		}
	}
	return "Unknown"
}
