package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestWorkerPickersExcludeIncompatibleModels(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "lfm3", ModelsDir: t.TempDir()}
	providers := []discovery.ProviderInfo{{Name: "ollama", Available: true, Models: []string{"dolphin", "lfm3", "embedding"}, WorkerModels: []string{"lfm3"}}}
	m := newSettings(cfg, providers)
	if got := m.workerModelChoices(); !reflect.DeepEqual(got, []string{"default", "lfm3"}) {
		t.Fatalf("worker choices = %v", got)
	}
	if len(m.modelItems) != 3 {
		t.Fatalf("worker filter changed general brain choices: %v", m.modelItems)
	}
	saves := 0
	m.saveConfig = func(string, any) error { saves++; return nil }
	m.setWorkerModel("dolphin")
	if saves != 0 || cfg.VoiceWorkerModel != "lfm3" {
		t.Fatal("a direct selection bypassed the worker filter")
	}
	m.ensureWorkerDraft()
	m.commitWorkerAssignment("simple", "ollama/dolphin")
	if !strings.Contains(m.message, "not available") {
		t.Fatalf("tier assignment bypassed filter: %s", m.message)
	}
	p := newPickerPersonas(t, cfg, nil)
	p.prefillStack(nil)
	p.workerProviderIdx = providerIndex(stackWorkerProviders(), "ollama")
	p.workerModel = "dolphin"
	p.applyBrainModels(personaModelsMsg{seq: p.brainModels.seq, providers: providers})
	if got := p.stackWorkerModelList(); !reflect.DeepEqual(got, []string{stackDefaultLabel, "lfm3"}) {
		t.Fatalf("saved incompatible pin became selectable: %v", got)
	}
	if p.workerModel != "dolphin" || !strings.Contains(p.workerModelLabel(), "tool support unavailable") {
		t.Fatal("invalid saved pin was silently rewritten or presented as verified")
	}
}
