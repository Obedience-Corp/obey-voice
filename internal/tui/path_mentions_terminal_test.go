package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

const pathDemoReply = "That's the obey-voice engine: the Go voice agent, its chat TUI and the providers it talks to."

// pathDemoBrain answers every turn with pathDemoReply. Everything else,
// including the history the Selector projects, is pauseTerminalBrain's.
type pathDemoBrain struct{ *pauseTerminalBrain }

func (b *pathDemoBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity := brain.TurnIdentity{}
	if opts.Identity != nil {
		identity = *opts.Identity
	}
	original := opts.OriginalInput
	if original == "" {
		original = input
	}
	b.mu.Lock()
	b.history = append(b.history,
		brain.Turn{Role: "user", Content: original, RoutedInput: input},
		brain.Turn{Role: "assistant", Content: pathDemoReply, PersonaID: identity.PersonaID, PersonaDisplayName: identity.PersonaDisplayName, PromptHash: identity.PromptHash, ResponseID: identity.ResponseID},
	)
	b.mu.Unlock()
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- pathDemoReply
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (b *pathDemoBrain) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	if _, err := b.ThinkStream(ctx, input, opts); err != nil {
		return "", err
	}
	return pathDemoReply, nil
}

// TestPathMentionsTerminal drives the real composer, Selector and Pipeline in
// a disposable campaign. Only the brain is a fixture: no microphone, playback
// or model. OBEY_VOICE_PATH_TERMINAL=record paces it for VHS.
func TestPathMentionsTerminal(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	config.SetConfigDirForTest(t, filepath.Join(tmp, "config"))
	campaign := filepath.Join(tmp, "campaign")
	for _, dir := range []string{".campaign/settings", "projects/camp/cmd", "projects/obey-voice/internal", "projects/obey-voice/pkg", "workflow/design", "docs"} {
		if err := os.MkdirAll(filepath.Join(campaign, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		".campaign/campaign.yaml":       "name: demo\n",
		"README.md":                     "demo campaign\n",
		"projects/obey-voice/README.md": "obey-voice\n",
		"projects/obey-voice/go.mod":    "module obey-voice\n",
		"projects/camp/go.mod":          "module camp\n",
	} {
		if err := os.WriteFile(filepath.Join(campaign, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Samantha works outside the campaign; @paths still resolve against the
	// conversation's starting directory, which is inside it.
	pinned := filepath.Join(tmp, "elsewhere")
	if err := os.MkdirAll(pinned, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []persona.Profile{pauseProofProfile("samantha", "Samantha"), pauseProofProfile("santa", "Santa")} {
		if p.ID == "samantha" {
			p.StartingDirectory = pinned
		}
		writePauseProofCatalog(t, config.ConfigDir(), p)
	}

	launch := filepath.Join(campaign, "projects", "obey-voice")
	base := &config.Config{
		ActivePersona: "samantha", AgentName: "Samantha", Persona: "samantha",
		BrainProvider: "ollama", OllamaModel: "samantha-fixture",
		PromptsDir: filepath.Join(config.ConfigDir(), "prompts"), WorkDir: launch, WorkDirSource: "launch-dir",
		VoiceWorkEnabled: true, VoiceWorkerProvider: "stub", EnvironmentContextEnabled: true,
	}
	catalog := persona.Catalog{Dir: filepath.Join(config.ConfigDir(), "personas")}
	initial, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	if config.WorkDirFrom(initial.Config()) != pinned {
		t.Fatalf("persona directory = %q, want %q", config.WorkDirFrom(initial.Config()), pinned)
	}
	grounding := brain.AssembleSystemPrompt(brain.SystemPromptInput{Provider: "ollama", Persona: "P", WorkDir: pinned, Cfg: initial.Config()})
	if !strings.Contains(grounding, "relative to "+campaign+";") {
		t.Fatalf("grounding does not name the campaign root %q:\n%s", campaign, grounding)
	}
	p := &pipeline.Pipeline{Brain: &pathDemoBrain{&pauseTerminalBrain{promptHash: "samantha-initial"}}, Events: events.NewBus(), VoiceToolsEnabled: false}
	jobs := job.NewTable(t.Context())
	agency := pipeline.NewAgency(p, jobs, nil, base, nil)
	agency.Wire()
	if err := p.EnableConversationWork("path-demo", nil, base); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(tmp, "sessions"))
	sess := session.New("ollama", "samantha-fixture")
	selector, err := personaruntime.NewSelector(personaruntime.SelectionOptions{
		Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: initial,
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
		},
		NewBrain: func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
			hash, err := brain.PersonaPromptHash(cfg)
			if err != nil {
				return nil, err
			}
			return &pathDemoBrain{&pauseTerminalBrain{promptHash: hash}}, nil
		},
		NewWorker: func(context.Context, *config.Config) (brain.Provider, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer selector.Close()
	defer func() {
		_ = p.CloseWork()
		_ = p.CloseBrains()
		jobs.Close()
	}()
	p.AdmitInput = selector.Route

	runner := &pauseTerminalRunner{pipeline: p, voiceStarts: make(chan int, 8), voiceEnds: make(chan int, 8), textStarts: make(chan struct{}, 1), textEnds: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	wg := &sync.WaitGroup{}
	m := newConversation(initial.DisplayName)
	m.cfg = initial.Config()
	m.setSize(100, 34)
	initCmd := m.startConversation(conversationDeps{
		runner: runner, bus: p.Events, ctx: ctx, wg: wg, sessionID: sess.ID,
		mentionPaths: mentionPathsFor(initial.Config()),
	})

	mode := os.Getenv("OBEY_VOICE_PATH_TERMINAL")
	h := &pathTerminal{model: m, initial: initCmd, keyDelay: time.Millisecond, pause: 20 * time.Millisecond}
	if mode == "record" {
		h.keyDelay, h.pause = 90*time.Millisecond, 2200*time.Millisecond
	}
	var output bytes.Buffer
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithoutSignalHandler()}
	if mode == "record" {
		opts = append(opts, tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	} else {
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(&output))
	}
	if _, err := tea.NewProgram(h, opts...).Run(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if h.err != "" {
		t.Fatal(h.err)
	}
	if h.step < len(h.script) {
		t.Fatalf("script stopped at step %d of %d", h.step, len(h.script))
	}
	views := strings.Join(h.views, "\n")
	for _, want := range []string{"compare @projects/", "@projects/camp/", "@samantha", "@santa", "@projects/obey-voice/README.md"} {
		if !strings.Contains(views, want) {
			t.Fatalf("terminal never showed %q", want)
		}
	}
	sent := "@projects/obey-voice/ what is this?"
	if got := runner.textInputs; len(got) == 0 || got[len(got)-1] != sent {
		t.Fatalf("pipeline received %q, want %q", got, sent)
	}
	last := h.model.plainTurns[len(h.model.plainTurns)-1]
	if last.role != "assistant" || last.text != pathDemoReply {
		t.Fatalf("last turn = %+v, want the fixture reply", last)
	}
	if strings.Contains(strings.Join(h.model.transcript, "\n"), "unknown persona") {
		t.Fatal("the @path message was refused as an unknown persona")
	}
}

type pathTerminalStep int

// pathTerminal types into the real conversation model one key at a time, so
// the shortcut rewrite fires exactly as it does for a person typing.
type pathTerminal struct {
	model    conversationModel
	initial  tea.Cmd
	keyDelay time.Duration
	pause    time.Duration
	script   []pathTerminalAction
	step     int
	views    []string
	err      string
}

type pathTerminalAction struct {
	key   tea.KeyMsg
	wait  time.Duration
	check func(*pathTerminal) bool
	note  string
}

func (h *pathTerminal) keys(text string) []pathTerminalAction {
	var out []pathTerminalAction
	for _, r := range text {
		out = append(out, pathTerminalAction{key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}, wait: h.keyDelay})
	}
	return out
}

func (h *pathTerminal) press(k tea.KeyType, wait time.Duration) pathTerminalAction {
	return pathTerminalAction{key: tea.KeyMsg{Type: k}, wait: wait}
}

func (h *pathTerminal) hold(note string, check func(*pathTerminal) bool) pathTerminalAction {
	return pathTerminalAction{wait: h.pause, check: check, note: note}
}

func (h *pathTerminal) buildScript() {
	var s []pathTerminalAction
	s = append(s, h.keys("compare @p/")...)
	s = append(s, h.hold("@p/ rewrote to @projects/ and listed it", func(h *pathTerminal) bool {
		return h.model.input.Value() == "compare @projects/" && h.model.mentionsShowing()
	}))
	s = append(s, h.press(tea.KeyTab, h.pause/3))
	s = append(s, h.hold("Tab accepted the highlighted directory", func(h *pathTerminal) bool {
		return h.model.input.Value() == "compare @projects/camp/"
	}))
	s = append(s, pathTerminalAction{key: tea.KeyMsg{Type: tea.KeyCtrlU}, wait: h.keyDelay, note: "clear"})
	s = append(s, h.keys("@")...)
	s = append(s, h.hold("@ at message start lists personas then paths", func(h *pathTerminal) bool {
		items := h.model.visibleMentions()
		return h.model.mentions.personas == 2 && len(items) > 2 && items[2].path != ""
	}))
	s = append(s, h.keys("p/")...)
	s = append(s, h.press(tea.KeyDown, h.pause/2))
	s = append(s, h.press(tea.KeyTab, h.pause/3))
	s = append(s, h.hold("Tab walked into obey-voice/", func(h *pathTerminal) bool {
		return h.model.input.Value() == "@projects/obey-voice/" && len(h.model.visibleMentions()) > 0
	}))
	s = append(s, h.keys(" what is this?")...)
	s = append(s, h.press(tea.KeyEnter, h.keyDelay))
	s = append(s, pathTerminalAction{wait: h.pause, note: "the persona answered", check: func(h *pathTerminal) bool {
		n := len(h.model.plainTurns)
		return n > 0 && h.model.plainTurns[n-1].role == "assistant" && h.model.turnState == turnIdle
	}})
	s = append(s, pathTerminalAction{wait: h.pause})
	h.script = s
}

func (h *pathTerminal) Init() tea.Cmd {
	h.buildScript()
	return tea.Batch(h.initial, h.next(h.pause/2))
}

func (h *pathTerminal) next(d time.Duration) tea.Cmd {
	step := h.step
	return tea.Tick(d, func(time.Time) tea.Msg { return pathTerminalStep(step) })
}

func (h *pathTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if step, ok := msg.(pathTerminalStep); ok && int(step) == h.step {
		if h.step >= len(h.script) {
			return h, tea.Quit
		}
		action := h.script[h.step]
		if action.check != nil && !action.check(h) {
			if action.note == "the persona answered" && strings.Contains(strings.Join(h.model.transcript, "\n"), "unknown persona") {
				h.err = "message refused as an unknown persona"
				return h, tea.Quit
			}
			return h, h.next(25 * time.Millisecond)
		}
		h.views = append(h.views, stripANSI(h.model.View()))
		h.step++
		var cmd tea.Cmd
		if action.key.Type != 0 || len(action.key.Runes) > 0 {
			if action.note == "clear" {
				previous := h.model.input.Value()
				h.model.input.Reset()
				h.model.syncEditorFromTextarea()
				h.model.syncComposer(previous)
			} else {
				h.model, cmd = h.model.Update(action.key)
			}
		}
		return h, tea.Batch(cmd, h.next(action.wait))
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyCtrlC {
		return h, tea.Quit
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	return h, cmd
}

func (h *pathTerminal) View() string { return h.model.View() }
