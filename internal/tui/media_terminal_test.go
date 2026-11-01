package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
func TestChatMediaTerminal(t *testing.T)         { runMediaTerminal(t, false, false) }
func TestAgentMediaTerminal(t *testing.T)        { runMediaTerminal(t, true, false) }
func TestChatMediaRejectedTerminal(t *testing.T) { runMediaTerminal(t, false, true) }
func TestChatMediaRejectedTerminalSlowValidation(t *testing.T) {
	runMediaTerminal(t, false, true, 250*time.Millisecond)
}

func runMediaTerminal(t *testing.T, agent, reject bool, validationDelay ...time.Duration) {
	config.SetConfigDirForTest(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "Screenshot with spaces.png")
	fixture := testmedia.PNG()
	if source := os.Getenv("OBEY_VOICE_MEDIA_FIXTURE"); source != "" {
		var err error
		fixture, err = os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	runner := &mediaTerminalRunner{bus: bus, agent: agent}
	if len(validationDelay) > 0 {
		runner.validationDelay = validationDelay[0]
	}
	if reject {
		runner.validationErr = errors.New("model lfm3:1.7b does not support images; select a vision-capable Ollama model")
	}
	if agent {
		root := t.TempDir()
		for name, data := range map[string][]byte{"result.png": testmedia.PNG(), "result.gif": testmedia.GIF()} {
			file := filepath.Join(root, name)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		text := "Two colored blocks inspected. Here are the returned image and animation.\n![Result](<" + filepath.Join(root, "result.png") + ">)\n![Animation](<" + filepath.Join(root, "result.gif") + ">)"
		var err error
		runner.reply, runner.output, err = chatmedia.DefaultStore().ImportReply(text, root)
		if err != nil {
			t.Fatal(err)
		}
	}
	m := sizedConversation(t, 100, 42)
	m.cfg = &config.Config{BrainProvider: "ollama", AgentName: "Obey"}
	initial := m.startConversation(conversationDeps{runner: runner, ctx: t.Context(), bus: bus})
	record := os.Getenv("OBEY_VOICE_MEDIA_TERMINAL") == "record"
	interactive := os.Getenv("OBEY_VOICE_MEDIA_TERMINAL") == "1"
	h := &mediaTerminal{model: m, initial: initial, path: path, scripted: !interactive, agent: agent, reject: reject, runner: runner, delay: 75 * time.Millisecond}
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
		for _, want := range []string{"Screenshot with spaces.png", "GIF (model input: first frame)", "Two colored blocks", "F4 remove"} {
			if !strings.Contains(view, want) {
				t.Fatalf("terminal missing %q", want)
			}
		}
		if agent {
			h.model.viewport.GotoBottom()
			view := stripANSI(h.model.viewport.View())
			if !strings.Contains(view, "result.gif") || !strings.Contains(view, "result.png") {
				t.Fatalf("returned previews absent from viewport: %s; items=%+v", view, h.model.items)
			}
		}
		if agent && (len(h.model.plainTurns) < 2 || len(h.model.plainTurns[len(h.model.plainTurns)-1].attachments) != 2) {
			t.Fatal("agent reply lost images in the thread")
		}
		if len(runner.refs) != 2 {
			t.Fatalf("model received %d images", len(runner.refs))
		}
	}
}

type mediaTerminalRunner struct {
	bus             *events.Bus
	agent           bool
	reply           string
	output          []chatmedia.Ref
	refs            []chatmedia.Ref
	validationErr   error
	validationMu    sync.RWMutex
	validationDelay time.Duration
}

func (r *mediaTerminalRunner) RunTurn(context.Context) (string, error) { return "", nil }
func (r *mediaTerminalRunner) ValidateImages(ctx context.Context, _ []chatmedia.Ref) error {
	r.validationMu.RLock()
	err := r.validationErr
	r.validationMu.RUnlock()
	if r.validationDelay > 0 {
		timer := time.NewTimer(r.validationDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
func (r *mediaTerminalRunner) RunTurnTextMode(ctx context.Context, text string) error {
	r.refs = chatmedia.Input(ctx)
	r.bus.Emit(events.UserInput{Text: text})
	if r.agent {
		r.bus.Emit(events.JobFinished{ID: "image-worker", Task: "Inspect and return images", Summary: r.reply, Attachments: r.output})
		return nil
	}
	r.bus.Emit(events.ResponseReady{Response: "Two colored blocks and an animated bar are attached. The GIF's first frame is used for model input."})
	return nil
}

type mediaTerminalStep int
type mediaTerminal struct {
	model    conversationModel
	initial  tea.Cmd
	path     string
	scripted bool
	agent    bool
	reject   bool
	runner   *mediaTerminalRunner
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
		if !h.readyForStep(step) {
			return h, h.next(int(step))
		}
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
		case 5, 6:
			if h.reject {
				msg = tea.KeyMsg{Type: tea.KeyEnter}
			} else if step == 5 {
				msg = tea.KeyMsg{Type: tea.KeyPgUp}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyEnd}
			}
		case 7, 8:
			if h.reject && step == 7 {
				h.runner.validationMu.Lock()
				h.runner.validationErr = nil
				h.runner.validationMu.Unlock()
				msg = tea.KeyMsg{Type: tea.KeyEnter}
				break
			}
			if !h.agent {
				return h, tea.Quit
			}
			msg = tea.KeyMsg{Type: tea.KeyF2}
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

// Script ticks pace the visual recording; readiness, rather than a fixed delay,
// decides when an asynchronous import/validation/response can be acted on.
func (h *mediaTerminal) readyForStep(step mediaTerminalStep) bool {
	switch step {
	case 1:
		return !h.model.media.importing && len(h.model.media.draft) == 1
	case 2, 3, 4:
		return !h.model.media.importing && len(h.model.media.draft) == 2
	case 5, 6:
		if h.reject {
			return !h.model.media.validating && h.model.media.err != ""
		}
	case 7:
		if h.reject {
			return !h.model.media.validating && h.model.media.err != ""
		}
		return h.hasReply()
	case 8:
		return h.hasReply()
	}
	return true
}
func (h *mediaTerminal) hasReply() bool {
	if len(h.model.plainTurns) < 2 {
		return false
	}
	last := h.model.plainTurns[len(h.model.plainTurns)-1]
	return strings.Contains(last.text, "Two colored blocks") && (!h.agent || len(last.attachments) == 2)
}
