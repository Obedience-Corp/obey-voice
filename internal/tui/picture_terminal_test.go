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
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The VHS recording drives this through the keyboard route: /image, choose an
// action, save a copy. The clipboard and viewer are fakes and the copy lands in
// a temporary home.
func TestPictureActionsTerminal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o700); err != nil {
		t.Fatal(err)
	}
	m, rig, harbor := pictureConversation(t, 100, 40, nil, func(harbor chatmedia.Ref) []chatItem {
		return []chatItem{sys(userStyle.Render("Milo: the harbor at dusk"), harbor)}
	})
	pier := storePicture(t, "pier.png", testmedia.PNG())
	m.agentName = "Obey"
	m.items = append(m.items, sys(agentStyle.Render("Vera: and the pier from the ferry"), pier))
	m.refreshContent()
	drainMedia(t, &m, m.mediaCommand())
	m.viewport.GotoBottom()

	record := os.Getenv("OBEY_VOICE_MEDIA_TERMINAL") == "record"
	h := &pictureScript{model: m, delay: 40 * time.Millisecond}
	if record {
		h.delay = 1400 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if record {
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(h, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	if h.model.pictures.open {
		t.Fatal("panel still open at the end")
	}
	store := chatmedia.DefaultStore()
	if len(rig.clip.pictures) != 1 || rig.clip.pictures[0] != (copiedPicture{store.Path(pier), "png"}) {
		t.Fatalf("copied %+v", rig.clip.pictures)
	}
	if _, err := os.Stat(filepath.Join(home, "Downloads", "harbor.png")); err != nil {
		t.Fatalf("copy not saved: %v", err)
	}
	if !record {
		screen := ansi.Strip(output.String())
		for _, want := range []string{"Picture 2 of 2", "Copied the picture", "Saved to ~/Downloads/harbor.png", harbor.Name} {
			if !strings.Contains(screen, want) {
				t.Fatalf("terminal never showed %q", want)
			}
		}
	}
}

type pictureStep int

type pictureScript struct {
	model conversationModel
	delay time.Duration
	// resume is the step to run once the action in flight has reported.
	resume int
}

func (h *pictureScript) Init() tea.Cmd { return h.next(0) }
func (h *pictureScript) next(n int) tea.Cmd {
	return tea.Tick(h.delay, func(time.Time) tea.Msg { return pictureStep(n) })
}
func (h *pictureScript) View() string { return h.model.View() }

// A step ending in * runs an action and waits for its result, however long the
// machine takes, before the next key.
var pictureSteps = []string{"/image", "enter", "down", "enter*", "left", "down", "down", "down", "enter", "enter*", "esc", "quit"}

func (h *pictureScript) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if step, ok := msg.(pictureStep); ok {
		s, wait := strings.CutSuffix(pictureSteps[step], "*")
		switch s {
		case "quit":
			return h, tea.Quit
		case "/image":
			h.model.input.SetValue(s)
			h.model.syncEditorFromTextarea()
			return h, h.next(int(step) + 1)
		default:
			msg = panelKey(s)
		}
		if wait {
			h.resume = int(step) + 1
		} else {
			next = h.next(int(step) + 1)
		}
	}
	if _, done := msg.(pictureActionDone); done && h.resume > 0 {
		next, h.resume = h.next(h.resume), 0
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyCtrlC {
		return h, tea.Quit
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, tea.Batch(cmd, next)
}
