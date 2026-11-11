package pipeline

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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
// or the live persona is unnamed, no other persona has been live or replied
// in the conversation for it to belong to.
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
	if p.Agency != nil {
		for _, id := range p.Agency.conversationPersonas() {
			seen[id] = true
		}
	}
	if len(seen) > 1 {
		return false
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

// conversationTurns is the conversation as the host records it, or nil when
// the live provider keeps it: that history belongs to the turn goroutine.
func (p *Pipeline) conversationTurns() []brain.Turn {
	if t := p.transcriptFor(p.Brain); t != nil {
		return t.Turns()
	}
	return nil
}

// notePersonaLocked records cfg's persona as one the conversation has had
// live. The caller holds a.mu.
func (a *Agency) notePersonaLocked(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if a.personas == nil {
		a.personas = make(map[string]bool)
	}
	a.personas[personaKey(cfg.ActivePersona)] = true
}

// conversationPersonas are the personas the conversation has had live.
func (a *Agency) conversationPersonas() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.personas))
	for id := range a.personas {
		ids = append(ids, id)
	}
	return ids
}

// restartPersonas keeps only the live persona when the conversation starts
// again.
func (a *Agency) restartPersonas() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.personas = nil
	a.notePersonaLocked(a.cfg)
}
