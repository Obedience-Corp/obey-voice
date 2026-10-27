package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type phononSetupTerminalState struct {
	view  string
	ready bool
}
type phononSetupTerminalHarness struct {
	model  settingsModel
	states chan phononSetupTerminalState
}

func (h *phononSetupTerminalHarness) Init() tea.Cmd { return nil }
func (h *phononSetupTerminalHarness) View() string  { return h.model.View() }
func (h *phononSetupTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := h.model.Update(msg)
	h.model = m
	if _, ok := msg.(settingsVoicesMsg); ok {
		// Select the named voice through normal key handling once setup has
		// refreshed the real provider's catalog.
		if i := voiceIndex(m.shownVoiceItems(), "Zoey"); i >= 0 && !m.speechSetup.running {
			h.model.cursor = m.voiceLine(i)
			h.states <- phononSetupTerminalState{view: m.View(), ready: true}
		}
	}
	if _, ok := msg.(tea.KeyMsg); ok {
		h.states <- phononSetupTerminalState{view: m.View()}
	}
	return h, cmd
}

// Real Bubble Tea event loop, production installer/provider and real config
// persistence. Only Ollama is replaced by an HTTP fixture; no speakers or
// personal config are used.
func TestPhononTerminalSelectionInstallsRefreshesAndPersists(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var pulls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"version":"0.40.11"}`) })
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"models":[{"name":"phonon:latest"}]}`) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":[{"id":"phonon"}]}`) })
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, _ *http.Request) {
		if pulls.Load() > 0 {
			fmt.Fprint(w, `{"voices":["default","Zoey","Easton"]}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	})
	mux.HandleFunc("/api/pull", func(w http.ResponseWriter, _ *http.Request) {
		pulls.Add(1)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	models := t.TempDir()
	if err := os.MkdirAll(filepath.Join(models, "phonon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(models, config.OpenAISpeechVoicesFile), []byte("{\"name\":\"Zoey\",\"locale\":\"en-us\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TTSProvider: "kokoro", OpenAISpeechURL: srv.URL + "/v1", ModelsDir: models}
	m := newSettings(cfg, nil)
	m.section, m.width, m.height = sectionVoice, 120, 45
	found := false
	for i := range m.sectionLines() {
		m.cursor = i
		row, line, _ := m.focused()
		if row.key == "tts_provider" && line.option >= 0 && strings.HasPrefix(row.options[line.option], "openai-speech") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Phonon engine absent from picker")
	}
	h := &phononSetupTerminalHarness{model: m, states: make(chan phononSetupTerminalState, 8)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() { p.Quit(); <-done }()
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	for {
		select {
		case state := <-h.states:
			if !state.ready {
				continue
			}
			if !strings.Contains(state.view, "Zoey") || !strings.Contains(state.view, "Install / refresh voices") {
				t.Fatalf("voices missing from terminal:\n%s", state.view)
			}
			goto ready
		case <-ctx.Done():
			t.Fatal("setup did not refresh the terminal voice list")
		}
	}
ready:
	if pulls.Load() != 1 {
		t.Fatalf("pulls = %d", pulls.Load())
	}
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-h.states:
	case <-ctx.Done():
		t.Fatal("voice selection timed out")
	}
	loaded, err := config.LoadWithoutHooks()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TTSProvider != config.OpenAISpeechProviderName || loaded.OpenAISpeechVoice != "Zoey" {
		t.Fatalf("persisted provider/voice = %s/%s", loaded.TTSProvider, loaded.OpenAISpeechVoice)
	}
}
