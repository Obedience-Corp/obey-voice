package tui

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestObeyCatalogChangesPreserveManualFieldBeforeEnterAndAfterSave(t *testing.T) {
	for _, key := range []string{"obey_provider", "obey_model", "obey_camp", "obey_socket", "obey_tool_policy"} {
		t.Run(key, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			cfg := &config.Config{BrainProvider: "obey", ObeySocket: "/fixture.sock", ObeyProvider: "fixture", ObeyToolPolicy: "strict"}
			m := newSettings(cfg, nil)
			m.section = sectionBrain
			m.visited[sectionBrain] = true
			m.width = 110
			m.height = 35
			m.cursor = lineOf(m, key, -1)
			m = m.receiveObeyCatalog(obeyCatalogMsg{seq: m.obeyCatalogSeq, route: discovery.ObeyRoute{Socket: cfg.ObeySocket, Provider: cfg.ObeyProvider, Model: cfg.ObeyModel, ToolPolicy: cfg.ObeyToolPolicy, Problem: "fixture catalog unavailable"}})
			row, _, _ := m.focused()
			if row.key != key {
				t.Fatalf("catalog redirected %s focus to %s", key, row.key)
			}
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.edit.decisionKey != key {
				t.Fatal("Enter opened a different field", m.edit.decisionKey)
			}
			m.cancelEdit()
			saves := 0
			m.saveConfig = func(saved string, value any) error {
				if saved != key {
					t.Fatal("saved wrong field", saved)
				}
				saves++
				return nil
			}
			m.commitObeyEdit(key, "fixture value")
			row, _, _ = m.focused()
			if saves != 1 || row.key != key {
				t.Fatal("catalog removal moved edited field", row.key, saves)
			}
		})
	}
}
