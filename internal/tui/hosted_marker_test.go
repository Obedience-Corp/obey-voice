package tui

import (
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/charmbracelet/x/ansi"
)

func TestAgentTurnFromAHostedBackendIsMarked(t *testing.T) {
	for _, hosted := range []bool{true, false} {
		item := chatItem{kind: chatItemAgent, name: "Mira", hosted: hosted, spoken: "Hello."}
		if got := strings.Contains(ansi.Strip(item.render(false)), "Mira [hosted]"); got != hosted {
			t.Errorf("hosted = %v: marker shown = %v", hosted, got)
		}
	}
}

func TestBrainBadgeMarksHostedBackends(t *testing.T) {
	for provider, want := range map[string]bool{"claude": true, "ollama": false} {
		m := conversationModel{cfg: &config.Config{BrainProvider: provider}}
		if got := strings.HasSuffix(m.brainBadgeLabel(), "· hosted"); got != want {
			t.Errorf("%s: badge %q hosted = %v, want %v", provider, m.brainBadgeLabel(), got, want)
		}
	}
}
