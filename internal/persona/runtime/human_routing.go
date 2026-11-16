package personaruntime

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"

func (s *Selector) humanSpeakerName(speaker string) string {
	configured := ""
	if s.opts.Base != nil {
		configured = s.opts.Base.UserDisplayName
	}
	return brain.HumanSpeakerName(speaker, s.opts.SpeakerNames, configured)
}
