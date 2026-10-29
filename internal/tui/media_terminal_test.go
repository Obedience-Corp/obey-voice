package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

// VHS uses the actual conversation model, paste messages and text admission.
// Only the provider is a fixture. No microphone, playback or external requests.
func TestChatMediaTerminal(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "Screenshot with spaces.png")
	if err := os.WriteFile(path, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	runner := &mediaTerminalRunner{bus: bus}
	m := sizedConversation(t, 100, 42)
	m.cfg = &config.Config{BrainProvider: "ollama", AgentName: "Obey"}
	initial := m.startConversation(conversationDeps{runner: runner, ctx: t.Context(), bus: bus})
	record := os.Getenv("OBEY_VOICE_MEDIA_TERMINAL") == "record"
	interactive := os.Getenv("OBEY_VOICE_MEDIA_TERMINAL") == "1"
	h := &mediaTerminal{model: m, initial: initial, path: path, scripted: !interactive, delay: 75 * time.Millisecond}
	if record {
		h.delay = 1800 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if record || interactive {
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(h, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	if !record && !interactive {
		view := stripANSI(output.String())
		for _, want := range []string{"Screenshot with spaces.png", "GIF (first frame sent)", "Two colored blocks", "F4 remove"} {
			if !strings.Contains(view, want) {
				t.Fatalf("terminal missing %q", want)
			}
		}
		if len(runner.refs) != 2 {
			t.Fatalf("model received %d images", len(runner.refs))
		}
	}
}

type mediaTerminalRunner struct {
	bus  *events.Bus
	refs []chatmedia.Ref
}

func (r *mediaTerminalRunner) RunTurn(context.Context) (string, error)               { return "", nil }
func (r *mediaTerminalRunner) ValidateImages(context.Context, []chatmedia.Ref) error { return nil }
func (r *mediaTerminalRunner) RunTurnTextMode(ctx context.Context, text string) error {
	r.refs = chatmedia.Input(ctx)
	r.bus.Emit(events.UserInput{Text: text})
	r.bus.Emit(events.ResponseReady{Response: "Two colored blocks and an animated bar are attached. The GIF's first frame is used for model input."})
	return nil
}

type mediaTerminalStep int
type mediaTerminal struct {
	model    conversationModel
	initial  tea.Cmd
	path     string
	scripted bool
	delay    time.Duration
}

func (h *mediaTerminal) Init() tea.Cmd {
	if h.scripted {
		return tea.Batch(h.initial, h.next(0))
	}
	return h.initial
}
func (h *mediaTerminal) next(n int) tea.Cmd {
	return tea.Tick(h.delay, func(time.Time) tea.Msg { return mediaTerminalStep(n) })
}
func (h *mediaTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if step, ok := msg.(mediaTerminalStep); ok {
		switch step {
		case 0:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.ReplaceAll(h.path, " ", "\\ ")), Paste: true}
		case 1:
			msg = clipboardPasteMsg{image: testmedia.GIF()}
		case 2:
			msg = tea.KeyMsg{Type: tea.KeyF2}
		case 3:
			msg = tea.KeyMsg{Type: tea.KeyF2}
		case 4:
			h.model.input.SetValue("What do these images show?")
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case 5:
			msg = tea.KeyMsg{Type: tea.KeyPgUp}
		case 6:
			msg = tea.KeyMsg{Type: tea.KeyPgDown}
		default:
			return h, tea.Quit
		}
		next = h.next(int(step) + 1)
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyCtrlC {
		return h, tea.Quit
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, tea.Batch(cmd, next)
}
func (h *mediaTerminal) View() string { return h.model.View() }
