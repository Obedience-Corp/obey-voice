package pipeline

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// personaKey names a persona the way an empty active_persona resolves.
func personaKey(id string) string {
	if id = strings.TrimSpace(id); id == "" {
		return persona.DefaultID
	}
	return id
}

// livePersona is the selected persona, or false when the pipeline has no
// configuration to name it.
func (p *Pipeline) livePersona() (string, bool) {
	if p.Agency == nil {
		return "", false
	}
	cfg := p.Agency.currentConfig()
	if cfg == nil {
		return "", false
	}
	return personaKey(cfg.ActivePersona), true
}

// startedByLivePersona reports whether a job admitted with admission may be
// shown to the live persona: the live persona started it, or, when the job
// or the live persona is unnamed, no other persona is in the conversation for
// it to belong to.
func (p *Pipeline) startedByLivePersona(admission map[string]string) bool {
	origin, tagged := admission["persona"]
	live, known := p.livePersona()
	if tagged && known {
		return personaKey(origin) == live
	}
	seen := map[string]bool{}
	if tagged {
		seen[personaKey(origin)] = true
	}
	if known {
		seen[live] = true
	}
	for _, t := range p.conversationTurns() {
		if (t.Role != "assistant" && t.Role != "samantha") || t.PersonaID == "" {
			continue
		}
		if seen[personaKey(t.PersonaID)] = true; len(seen) > 1 {
			return false
		}
	}
	return true
}

// conversationTurns is the whole conversation as the host or the live
// provider records it.
func (p *Pipeline) conversationTurns() []brain.Turn {
	if t := p.transcriptFor(p.Brain); t != nil {
		return t.Turns()
	}
	if p.Brain == nil {
		return nil
	}
	if canonical, ok := p.Brain.(brain.CanonicalHistoryProvider); ok {
		return canonical.CanonicalHistory()
	}
	return p.Brain.History()
}
