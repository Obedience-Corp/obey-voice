package tui

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestSavedWorkerSettingsReachRunningConversation(t *testing.T) {
	cfg := &config.Config{ActivePersona: "santa"}
	p := &pipeline.Pipeline{}
	p.Agency = pipeline.NewAgency(p, nil, nil, cfg, nil)
	app := App{cfg: cfg, runtime: &ConversationRuntime{Pipeline: p}, screen: screenSettings}
	w := config.DefaultVoiceWorkers()
	w.Mode, w.Revision = config.WorkerModeTiered, 1
	model, _ := app.Update(workerSettingsSavedMsg{workers: w})
	updated := model.(App)
	if updated.runtime != app.runtime || p.Agency.WorkerSettings().Revision != 1 {
		t.Fatal("saved worker policy did not reach existing runtime")
	}
	w.Revision = 3
	if p.Agency.WorkerSettings().Revision != 1 {
		t.Fatal("runtime aliases Settings message")
	}
	if cfg.ActivePersona != "santa" {
		t.Fatal("worker settings changed selected persona")
	}
}
