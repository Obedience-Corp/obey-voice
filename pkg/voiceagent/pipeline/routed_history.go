package pipeline

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

// appendRoutedHistory commits host-authored replies through the same canonical
// projection used by persona acknowledgements. It never imports event history
// or changes the conversation/work lifetime.
func (p *Pipeline) appendRoutedHistory(metrics *turnMetrics, request, reply string) error {
	projection, ok := p.Brain.(brain.HistoryProjectionProvider)
	if !ok {
		if metrics.responseIdentity() != nil {
			return fmt.Errorf("routed reply requires canonical history projection")
		}
		return nil // Legacy providers retain their existing history ownership.
	}
	turns := p.Brain.History()
	now := time.Now().UTC()
	// A route chosen after a model turn already owns its input. A deterministic
	// route has no new user turn, even when the preceding input was identical.
	alreadyAppended := false
	for i := metrics.historyAtAdmission; i < len(turns); i++ {
		if turns[i].Role == "user" {
			alreadyAppended = true
			break
		}
	}
	if !alreadyAppended {
		input := brain.Turn{Role: "user", Content: metrics.originalInput, Speaker: p.currentSpeakerID(), At: now}
		if request != input.Content {
			input.RoutedInput = request
		}
		turns = append(turns, input)
	}
	if reply != "" {
		response := brain.Turn{Role: "assistant", Content: reply, At: now}
		if identity := metrics.responseIdentity(); identity != nil {
			response.PersonaID, response.PersonaDisplayName, response.PromptHash, response.ResponseID = identity.PersonaID, identity.PersonaDisplayName, identity.PromptHash, identity.ResponseID
		}
		turns = append(turns, response)
	}
	if err := projection.ImportHistory(turns); err != nil {
		return fmt.Errorf("record routed reply: %w", err)
	}
	return nil
}
