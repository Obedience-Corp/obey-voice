package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestMuseSparkSettingsTerminalPersistsModel(t *testing.T) {
	path := realWriterInstall(t, "brain_provider: muse-spark\nmuse_spark_model: old-model\n")
	cfg, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	m := newSettings(cfg, []discovery.ProviderInfo{{Name: "muse-spark", Available: true, Models: []string{"old-model", "new-model"}}})
	m.section, m.width, m.height = sectionBrain, 120, 40
	m.cursor = lineOf(m, "muse_spark_model", 1)
	if m.cursor < 0 {
		t.Fatal("Muse model is not selectable")
	}
	h := &workerTerminalHarness{model: m, states: make(chan workerTerminalState, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(h), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		p.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("terminal did not stop")
		}
	}()
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case state := <-h.states:
		if strings.Contains(state.message, "Failed") {
			t.Fatal(state.message)
		}
	case <-ctx.Done():
		t.Fatal("key timed out")
	}
	if !strings.Contains(readWritten(t, path), "muse_spark_model: new-model") {
		t.Fatal("model did not persist")
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil || loaded.MuseSparkModel != "new-model" || loaded.BrainProvider != "muse-spark" {
		t.Fatalf("model=%v err=%v", loaded, err)
	}
}

func TestMuseSparkDefaultModelPersistsEmpty(t *testing.T) {
	path := realWriterInstall(t, "brain_provider: muse-spark\nmuse_spark_model: chosen\n")
	cfg := &config.Config{BrainProvider: "muse-spark", MuseSparkModel: "chosen"}
	m := newSettings(cfg, nil)
	m.selectModel("default")
	loaded, err := config.LoadWithoutHooks()
	if err != nil || loaded.MuseSparkModel != "" || cfg.MuseSparkModel != "" {
		t.Fatalf("model=%q config=%v err=%v file=%s", cfg.MuseSparkModel, loaded, err, readWritten(t, path))
	}
}
