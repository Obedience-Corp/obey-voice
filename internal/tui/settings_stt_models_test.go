package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSTTModelSelectionScopesInstallAndHandlesFailureRetry(t *testing.T) {
	m, saved := sttModel(t, &config.Config{VADEnabled: true, ParakeetModel: "tdt-0.6b-v2"})
	attempts := 0
	release := make(chan struct{})
	m.sttSetup.ensure = func(_ context.Context, cfg *config.Config, req config.AssetRequest, progress func(config.AssetProgress)) error {
		attempts++
		if cfg == m.cfg || cfg.ParakeetModel != "tdt-0.6b-v3" || !req.NeedSTT || !req.NeedVAD || req.NeedTTS || req.NeedDecision || req.NeedSpeaker {
			t.Fatalf("wrong model or installer scope: %+v, %+v", cfg, req)
		}
		manifest, err := config.ManifestFor(cfg, req)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, asset := range manifest.Assets {
			if asset.Name == "tdt-0.6b-v3" {
				found = true
			}
		}
		if !found {
			t.Fatal("selected multilingual model was not in install manifest")
		}
		progress(config.AssetProgress{Asset: "tdt-0.6b-v3", Phase: "download", Pct: 50})
		if attempts == 1 {
			<-release
			return errors.New("checksum mismatch")
		}
		return nil
	}
	batch := m.selectSTTModel("parakeet_model", "tdt-0.6b-v3")().(tea.BatchMsg)
	if saved["parakeet_model"] != "tdt-0.6b-v3" {
		t.Fatal("choice was not saved")
	}
	completed := make(chan tea.Msg, 1)
	go func() { completed <- batch[1]() }()
	m.applySTTSetup(batch[0]())
	if !strings.Contains(m.message, "50%") {
		t.Fatal("download progress missing")
	}
	close(release)
	m.applySTTSetup(<-completed)
	if m.sttSetup.running || !strings.Contains(m.message, "checksum mismatch") || strings.Contains(m.message, "installed and verified") {
		t.Fatal(m.message)
	}
	retry := m.startSTTSetup()().(tea.BatchMsg)
	m.applySTTSetup(retry[1]())
	if attempts != 2 || !strings.Contains(m.message, "installed and verified") {
		t.Fatal(m.message)
	}
}

// A real Bubble Tea loop exercises key selection, progress, completion and the
// production config writer. The downloader is replaced with an isolated fixture.
type sttTerminalHarness struct {
	model  settingsModel
	states chan string
}

func (h *sttTerminalHarness) Init() tea.Cmd { return nil }
func (h *sttTerminalHarness) View() string  { return h.model.View() }
func (h *sttTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := h.model.Update(msg)
	h.model = model
	if _, ok := msg.(sttSetupDoneMsg); ok {
		h.states <- stripANSI(h.model.View())
	}
	return h, cmd
}

func TestSTTMultilingualTerminalSelectionPersistsAndInstalls(t *testing.T) {
	path := realWriterInstall(t, "# preserve me\nstt_provider: parakeet\nparakeet_model: tdt-0.6b-v2\n")
	cfg, err := config.LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelsDir = t.TempDir()
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionSTT, 120, 35
	m.sttSetup.ensure = func(_ context.Context, snapshot *config.Config, req config.AssetRequest, progress func(config.AssetProgress)) error {
		if snapshot.ParakeetModel != "tdt-0.6b-v3" || !req.NeedSTT || req.NeedTTS {
			return errors.New("wrong install request")
		}
		progress(config.AssetProgress{Asset: "tdt-0.6b-v3", Phase: "download", Pct: 50})
		return os.WriteFile(filepath.Join(snapshot.ModelsDir, "installed-model"), []byte(snapshot.ParakeetModel), 0600)
	}
	found := false
	for i := range m.sectionLines() {
		m.cursor = i
		row, line, ok := m.focused()
		if ok && row.key == "parakeet_model" && line.option >= 0 && strings.Contains(row.options[line.option], "Multilingual (25 languages)") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("multilingual choice missing from STT settings")
	}
	h := &sttTerminalHarness{model: m, states: make(chan string, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() { p.Quit(); <-done }()
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case view := <-h.states:
		if !strings.Contains(view, "installed and verified") {
			t.Fatal(view)
		}
	case <-ctx.Done():
		t.Fatal("model install did not finish")
	}
	if body := readWritten(t, path); !strings.Contains(body, "parakeet_model: tdt-0.6b-v3") || !strings.Contains(body, "# preserve me") {
		t.Fatal(body)
	}
	reloaded, err := config.LoadRaw()
	if err != nil || reloaded.ParakeetModel != "tdt-0.6b-v3" {
		t.Fatalf("saved model did not survive reload: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(cfg.ModelsDir, "installed-model"))
	if err != nil || string(marker) != "tdt-0.6b-v3" {
		t.Fatal("installer did not receive multilingual selection")
	}
}

func TestSTTModelInvalidChoiceAndSaveFailureDoNotInstall(t *testing.T) {
	m, saved := sttModel(t, &config.Config{})
	if cmd := m.selectSTTModel("parakeet_model", "missing"); cmd != nil || len(saved) > 0 {
		t.Fatal("invalid selection saved or installed")
	}
	m.saveConfig = func(string, any) error { return errors.New("disk full") }
	if cmd := m.selectSTTModel("parakeet_model", "tdt-0.6b-v3"); cmd != nil || m.cfg.ParakeetModel != "" {
		t.Fatal("failed save changed or installed model")
	}
}

func TestSTTInstallCancelFencesLateProgressAndCompletion(t *testing.T) {
	m, _ := sttModel(t, &config.Config{})
	m.sttSetup.ensure = func(ctx context.Context, _ *config.Config, _ config.AssetRequest, _ func(config.AssetProgress)) error {
		return ctx.Err()
	}
	batch := m.startSTTSetup()().(tea.BatchMsg)
	ctx, seq := m.sttSetup.ctx, m.sttSetup.seq
	m.closePreview()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("closing settings did not cancel installation")
	}
	m.message = "new selection"
	if m.applySTTSetup(batch[1]()) != nil || m.applySTTSetup(sttSetupProgressMsg{seq: seq}) != nil || m.message != "new selection" {
		t.Fatal("canceled worker changed settings")
	}
}

func TestSTTWhisperCPPSelectionClearsPreviousFileOverride(t *testing.T) {
	m, saved := sttModel(t, &config.Config{STTProvider: "whispercpp", WhisperCPPModelPath: "/old/ggml-base.en.bin"}, "whisper-cli")
	m.sttSetup.ensure = func(_ context.Context, cfg *config.Config, _ config.AssetRequest, _ func(config.AssetProgress)) error {
		if cfg.WhisperCPPModel != "small.en" || cfg.WhisperCPPModelPath != "" {
			t.Fatal("previous file still overrides selected model")
		}
		return nil
	}
	batch := m.selectSTTModel("whispercpp_model", "small.en")().(tea.BatchMsg)
	m.applySTTSetup(batch[1]())
	if saved["whispercpp_model"] != "small.en" || saved["whispercpp_model_path"] != "" {
		t.Fatalf("saved %v", saved)
	}
}
