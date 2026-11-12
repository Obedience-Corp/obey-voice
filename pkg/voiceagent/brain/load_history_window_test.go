package brain

import (
	"fmt"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Scenes load a briefing followed by the replayed lines; nothing may be
// dropped however long the replay is.
func TestPromptProvidersLoadEveryTurn(t *testing.T) {
	history := []Turn{{Role: "user", Content: "Briefing: you are on the bridge."}}
	for i := range 10 {
		history = append(history, Turn{Role: "user", Content: fmt.Sprintf("line %d", i)})
	}
	cfg := &config.Config{MaxHistory: 2}
	for name, p := range map[string]Provider{"claude": &Brain{cfg: cfg}, "grok": &GrokBrain{cfg: cfg}, "cursor": &CursorBrain{cfg: cfg}} {
		p.LoadHistory(history)
		if got := p.History(); len(got) != len(history) || got[0].Content != history[0].Content {
			t.Errorf("%s loaded %d of %d turns, first %q", name, len(got), len(history), got[0].Content)
		}
	}
}
