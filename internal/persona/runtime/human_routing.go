package personaruntime

import "strings"

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
