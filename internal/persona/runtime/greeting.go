package personaruntime

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// A greeting uses the existing exact-control selection/revalidation boundary.
// This check never selects or overrides a persona on its own.
func addressedGreeting(input string, roster *summon.Roster, exact summon.ExactControl) bool {
	if exact.Address.Bare {
		return false
	}
	if exact.Address.Reason == "explicit-mention" {
		return standaloneGreeting(exact.Address.Remainder)
	}
	address, err := roster.Resolve(input)
	return err == nil && address.PersonaID == exact.Address.PersonaID && standaloneGreeting(address.Remainder)
}

func (s *Selector) greetingReply(envelope events.ResponseEnvelope) string {
	text, err := prompts.RenderSystem(s.Config().PromptsDir, "persona-greeting", map[string]string{"agent_name": envelope.DisplayName})
	if err != nil && s.opts.Pipeline.Events != nil {
		s.opts.Pipeline.Events.Emit(events.Info{Message: err.Error()})
	}
	return text
}

// Match whole greetings only. A task/status question, compound greeting plus
// an action, quoted greeting, or uncertain wording remains an ordinary request.
func standaloneGreeting(input string) bool {
	text := strings.ToLower(strings.TrimSpace(input))
	text = strings.ReplaceAll(text, "’", "'")
	text = strings.Trim(text, ".!?,;:")
	text = strings.Join(strings.Fields(text), " ")
	switch text {
	case "hi", "hello", "hey", "good morning", "good afternoon", "good evening",
		"how are you", "how are you today", "how's it going", "how's it going today",
		"hows it going", "hows it going today":
		return true
	}
	return false
}
