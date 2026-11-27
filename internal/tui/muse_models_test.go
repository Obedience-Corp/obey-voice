package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Opening Settings with muse-spark selected reads Muse's catalog once and
// offers it in the Model row; choosing a row saves muse_spark_model.
func TestMuseSparkSettingsListsTheCatalog(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	stub := discoverProviders
	discoverProviders = func(*config.Config) []discovery.ProviderInfo {
		return []discovery.ProviderInfo{
			{Name: "claude", Available: true, Models: []string{"default"}, ModelsPending: true},
			{Name: "muse-spark", Available: true, Models: []string{"default"}, ModelsPending: true},
		}
	}
	t.Cleanup(func() { discoverProviders = stub })
	reads := map[string]int{}
	stubModels := discoverProviderModels
	discoverProviderModels = func(_ context.Context, _ *config.Config, name string) discovery.ProviderInfo {
		reads[name]++
		models := map[string][]string{"claude": {"default", "opus"}, "muse-spark": {"default", "model-b", "model-a"}}[name]
		return discovery.ProviderInfo{Name: name, Available: true, Models: models}
	}
	t.Cleanup(func() { discoverProviderModels = stubModels })

	app := NewApp(&config.Config{BrainProvider: "muse-spark", OllamaHost: "http://127.0.0.1:1"})
	opened, cmd := app.onSwitchScreen(switchScreenMsg(screenSettings))
	app = opened.(App)
	if row := app.settings.modelRows()[0]; !strings.Contains(row.detail, "reading the model list from the muse-spark CLI") {
		t.Fatalf("loading Model row = %+v", row)
	}
	var loaded []tea.Msg
	for _, msg := range flattenCmd(cmd) {
		if m, ok := msg.(providerModelsMsg); ok {
			loaded = append(loaded, m)
		}
	}
	if len(loaded) != 2 || reads["muse-spark"] != 1 || reads["claude"] != 1 {
		t.Fatalf("opening Settings read %v (%d messages), want each pending provider once", reads, len(loaded))
	}
	for _, msg := range loaded {
		model, _ := app.Update(msg)
		app = model.(App)
	}
	want := []string{"default", "model-b", "model-a"}
	row := app.settings.modelRows()[0]
	if !slices.Equal(row.options, want) || row.detail != "" {
		t.Fatalf("Settings Model row = %+v, want options %q", row, want)
	}

	row.set(&app.settings, 2)
	saved, err := config.LoadRaw()
	if err != nil || app.settings.cfg.MuseSparkModel != "model-a" || saved.MuseSparkModel != "model-a" {
		t.Fatalf("model = %q, saved = %q, err = %v", app.settings.cfg.MuseSparkModel, saved.MuseSparkModel, err)
	}

	_, cmd = app.onSwitchScreen(switchScreenMsg(screenSettings))
	for _, msg := range flattenCmd(cmd) {
		if _, ok := msg.(providerModelsMsg); ok {
			t.Fatal("reopening Settings read a model list again")
		}
	}
}
