package tui

import (
	"context"
	"encoding/json"
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

// terminalPersonaSnapshot is published by the test-only adapter after real
// Bubble Tea updates. The assertions consume snapshots instead of peeking at
// App while the program's update goroutine is running.
type terminalPersonaSnapshot struct {
	screen     screen
	voice      bool
	input      string
	agent      string
	sessionID  string
	selection  events.PersonaSelection
	transcript []string
	view       string
}

// terminalPersonaApp is deliberately a thin test adapter around the owning
// App. It starts the already-wired conversation and records rendered state;
// every input below still enters through tea.Program.Send.
type terminalPersonaApp struct {
	app      App
	start    tea.Cmd
	ready    chan struct{}
	updates  chan terminalPersonaSnapshot
	captured []terminalPersonaSnapshot
}

func (h *terminalPersonaApp) Init() tea.Cmd {
	close(h.ready)
	return h.start
}

func (h *terminalPersonaApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := h.app.Update(msg)
	if next, ok := model.(App); ok {
		h.app = next
	}
	switch msg.(type) {
	case tea.KeyMsg, switchScreenMsg, personaSelectionResultMsg, voiceTurnDoneMsg, textTurnDoneMsg, busEventMsg:
		selection := events.PersonaSelection{}
		if h.app.runtime != nil && h.app.runtime.SelectionSnapshot != nil {
			selection = h.app.runtime.SelectionSnapshot()
		}
		snapshot := terminalPersonaSnapshot{
			screen:     h.app.screen,
			voice:      h.app.conversation.voiceEnabled,
			input:      h.app.conversation.input.Value(),
			agent:      h.app.conversation.agentName,
			sessionID:  h.app.conversation.sessionID,
			selection:  selection,
			transcript: append([]string(nil), h.app.conversation.transcript...),
			view:       stripANSI(h.app.View()),
		}
		select {
		case h.updates <- snapshot:
		default:
		}
		h.captured = append(h.captured, snapshot)
	}
	return h, cmd
}

func (h *terminalPersonaApp) View() string { return h.app.View() }

func awaitTerminalSnapshot(t *testing.T, updates <-chan terminalPersonaSnapshot, label string, want func(terminalPersonaSnapshot) bool) terminalPersonaSnapshot {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-updates:
			if want(got) {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
			return terminalPersonaSnapshot{}
		}
	}
}

func sendTerminalText(program *tea.Program, text string) {
	for _, r := range text {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func sendTerminalEnter(program *tea.Program, text string) {
	sendTerminalText(program, text)
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

// TestTerminalPersonaRecoveryProof drives the real top-level terminal App.
// Its brain and listener are controlled local fixtures, so this is a terminal
// routing/persistence proof rather than the task03 Ollama/audio proof.
func TestTerminalPersonaRecoveryProof(t *testing.T) {
	// Register this check before SetConfigDirForTest: its cleanup runs after the
	// config helper restores the process-global path and proves no user config
	// file was touched by the manager.
	realConfig := config.ConfigFile()
	beforeConfig, beforeErr := os.ReadFile(realConfig)
	t.Cleanup(func() {
		afterConfig, afterErr := os.ReadFile(realConfig)
		switch {
		case beforeErr == nil && (afterErr != nil || string(afterConfig) != string(beforeConfig)):
			t.Errorf("user config changed during terminal proof: before=%v after=%v", beforeErr, afterErr)
		case os.IsNotExist(beforeErr) && afterErr == nil:
			t.Errorf("terminal proof created previously absent user config %s", realConfig)
		case os.IsNotExist(beforeErr) && afterErr != nil && !os.IsNotExist(afterErr):
			t.Errorf("user config read state changed: before absent, after=%v", afterErr)
		case beforeErr != nil && !os.IsNotExist(beforeErr) && afterErr == nil:
			t.Errorf("terminal proof changed unreadable user config into a file: before=%v", beforeErr)
		}
	})

	root, _ := filepath.EvalSymlinks(t.TempDir())
	pinned, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	for _, p := range []persona.Profile{pauseProofProfile("samantha", "Samantha"), pauseProofProfile("santa", "Santa")} {
		if p.ID == "samantha" {
			p.StartingDirectory = pinned
		}
		writePauseProofCatalog(t, config.ConfigDir(), p)
	}

	base := &config.Config{
		ActivePersona: "samantha", AgentName: "Samantha", Persona: "samantha",
		BrainProvider: "ollama", OllamaModel: "samantha-fixture",
		PromptsDir: filepath.Join(config.ConfigDir(), "prompts"), WorkDir: root,
		VoiceWorkEnabled: true, VoiceWorkerProvider: "stub",
	}
	catalog := persona.Catalog{Dir: filepath.Join(config.ConfigDir(), "personas")}
	initial, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	seedHistory := []brain.Turn{
		{Role: "user", Content: "The canonical context is blue comet."},
		{Role: "assistant", Content: "Samantha stored blue comet.", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "seed-samantha-response"},
	}
	old := &pauseTerminalBrain{promptHash: "samantha-initial", history: seedHistory}
	p := &pipeline.Pipeline{Brain: old, Events: events.NewBus(), VoiceToolsEnabled: false}
	jobs := job.NewTable(t.Context())
	agency := pipeline.NewAgency(p, jobs, nil, base, nil)
	agency.Wire()
	if err := p.EnableConversationWork("terminal-proof", nil, base); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "samantha-fixture")
	factory := func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return &pauseTerminalBrain{promptHash: hash}, nil
	}
	selector, err := personaruntime.NewSelector(personaruntime.SelectionOptions{
		Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: initial,
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash, selected.Directories)
		},
		NewBrain: factory,
		NewWorker: func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
			if cfg.WorkDir != root {
				t.Errorf("Santa worker directory = %q, want %q", cfg.WorkDir, root)
			}
			return nil, nil
		},
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
	p.OnTurn = func() {
		if err := store.Save(sess, p.Brain.History()); err != nil {
			t.Errorf("save turn: %v", err)
		}
	}
	selectionBefore := selector.Snapshot()
	if selectionBefore.WorkDir != pinned {
		t.Fatalf("initial persona directory = %q, want %q", selectionBefore.WorkDir, pinned)
	}

	runner := &pauseTerminalRunner{
		pipeline: p, voiceStarts: make(chan int, 8), voiceEnds: make(chan int, 8),
		textStarts: make(chan struct{}, 1), textEnds: make(chan struct{}, 1),
	}
	rt := &ConversationRuntime{
		Pipeline: p, Bus: p.Events, Voice: true, PersonaID: initial.PersonaID,
		AgentName: initial.DisplayName, SessionCfg: initial.Config(), SessionID: sess.ID,
		SelectPersona: func(ctx context.Context, id string) (events.PersonaSelection, *config.Config, error) {
			selected, err := selector.Select(ctx, id, "terminal-ui")
			if err != nil {
				return events.PersonaSelection{}, nil, err
			}
			return selected, selector.Config(), nil
		},
		SelectionSnapshot: selector.Snapshot,
		SelectionConfig:   selector.Config,
	}
	ctx, cancel := context.WithCancel(t.Context())
	wg := &sync.WaitGroup{}
	app := newApp(base, nil, nil)
	app.screen, app.cfg, app.width, app.height = screenConversation, base, 100, 30
	app.runCtx, app.wg, app.runtime = ctx, wg, rt
	app.conversation = newConversation(initial.DisplayName)
	app.conversation.cfg = initial.Config()
	app.conversation.setSize(100, 30)
	app.conversation.seedTranscript(seedHistory)
	start := app.conversation.startConversation(conversationDeps{runner: runner, bus: rt.Bus, voice: true, ctx: ctx, wg: wg, sessionID: sess.ID})
	app.conversation.configureSelection(rt)
	app.conversation.applySelectionSnapshot(selectionBefore)
	h := &terminalPersonaApp{app: app, start: start, ready: make(chan struct{}), updates: make(chan terminalPersonaSnapshot, 256)}
	var terminal strings.Builder
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(&terminal), tea.WithContext(ctx), tea.WithoutSignalHandler())
	runErr := make(chan error, 1)
	go func() {
		_, err := program.Run()
		runErr <- err
	}()
	t.Cleanup(func() {
		cancel()
		program.Quit()
		select {
		case err := <-runErr:
			if err != nil && !strings.Contains(err.Error(), "program was killed: context canceled") {
				t.Errorf("terminal shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for terminal shutdown")
		}
		wg.Wait()
		if dir := strings.TrimSpace(os.Getenv("VS0001_TUI_PROOF_DIR")); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Errorf("create TUI proof artifact directory: %v", err)
				return
			}
			type artifactSnapshot struct {
				Screen     int                     `json:"screen"`
				Voice      bool                    `json:"voice"`
				Input      string                  `json:"input"`
				Agent      string                  `json:"agent"`
				SessionID  string                  `json:"session_id"`
				Selection  events.PersonaSelection `json:"selection"`
				Transcript []string                `json:"transcript"`
				View       string                  `json:"view"`
			}
			artifacts := make([]artifactSnapshot, 0, len(h.captured))
			for _, snapshot := range h.captured {
				artifacts = append(artifacts, artifactSnapshot{
					Screen: int(snapshot.screen), Voice: snapshot.voice, Input: snapshot.input,
					Agent: snapshot.agent, SessionID: snapshot.sessionID,
					Selection: snapshot.selection, Transcript: snapshot.transcript, View: snapshot.view,
				})
			}
			data, err := json.MarshalIndent(artifacts, "", "  ")
			if err != nil {
				t.Errorf("marshal TUI rendered snapshots: %v", err)
			} else if err := os.WriteFile(filepath.Join(dir, "rendered-snapshots.json"), data, 0o600); err != nil {
				t.Errorf("write TUI rendered snapshots: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "terminal-output.txt"), []byte(terminal.String()), 0o600); err != nil {
				t.Errorf("write TUI terminal output: %v", err)
			}
		}
	})
	select {
	case <-h.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal program did not initialize")
	}
	if got := awaitPauseProof(t, runner.voiceStarts, "initial listening"); got != 1 {
		t.Fatalf("initial voice call = %d, want 1", got)
	}

	// Help and completion are both exercised through the actual composer.
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceEnds, "initial pause"); got != 1 {
		t.Fatalf("initial voice end = %d, want 1", got)
	}
	sendTerminalText(program, "/he")
	completed := awaitTerminalSnapshot(t, h.updates, "slash completion", func(s terminalPersonaSnapshot) bool { return s.input == "/he" })
	_ = completed
	program.Send(tea.KeyMsg{Type: tea.KeyTab})
	completed = awaitTerminalSnapshot(t, h.updates, "completed help command", func(s terminalPersonaSnapshot) bool { return s.input == "/help " })
	if completed.view == "" || !strings.Contains(completed.view, "/help") || !strings.Contains(completed.view, "Samantha stored blue comet.") {
		t.Fatalf("completion view omitted help command:\n%s", completed.view)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	help := awaitTerminalSnapshot(t, h.updates, "help output", func(s terminalPersonaSnapshot) bool {
		return strings.Contains(strings.Join(s.transcript, "\n"), "Slash commands:") && strings.Contains(s.view, "/personas") && strings.Contains(s.view, "/vim")
	})
	if !strings.Contains(help.view, "/personas") || !strings.Contains(help.view, "/vim") {
		t.Fatalf("help output missing registered commands:\n%s", help.view)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceStarts, "help resume"); got != 2 {
		t.Fatalf("help resumed voice call = %d, want 2", got)
	}

	// Vim is driven by terminal keys: enter insert mode, type, return to normal,
	// undo, then use a slash command from NORMAL mode.
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	awaitPauseProof(t, runner.voiceEnds, "help pause")
	sendTerminalEnter(program, "/vim on")
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceStarts, "vim resume"); got != 3 {
		t.Fatalf("vim resumed voice call = %d, want 3", got)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	awaitPauseProof(t, runner.voiceEnds, "vim pause")
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	sendTerminalText(program, "draft")
	program.Send(tea.KeyMsg{Type: tea.KeyEsc})
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	vim := awaitTerminalSnapshot(t, h.updates, "Vim undo", func(s terminalPersonaSnapshot) bool { return s.input == "" && strings.Contains(s.view, "NORMAL") })
	if !strings.Contains(vim.view, "esc normal") && !strings.Contains(vim.view, "i/a insert") {
		t.Fatalf("Vim terminal mode indicator missing:\n%s", vim.view)
	}

	// /personas is opened while the controlled listening turn is in flight, so
	// the real App switch path must cancel it and remember that it should resume.
	sendTerminalEnter(program, "/unmute")
	if got := awaitPauseProof(t, runner.voiceStarts, "unmute resume"); got != 4 {
		t.Fatalf("unmute resumed voice call = %d, want 4", got)
	}
	sendTerminalEnter(program, "/personas")
	manager := awaitTerminalSnapshot(t, h.updates, "personas manager", func(s terminalPersonaSnapshot) bool { return s.screen == screenPersonas })
	if manager.voice || !strings.Contains(manager.view, "Samantha") || !strings.Contains(manager.view, "Santa") {
		t.Fatalf("manager did not render both personas with mic paused:\n%s", manager.view)
	}
	if got := awaitPauseProof(t, runner.voiceEnds, "manager opening cancellation"); got != 4 {
		t.Fatalf("manager canceled voice call = %d, want 4", got)
	}

	// First cancel the manager without selecting anything. This proves the
	// open/cancel path preserves Samantha and the session before the switch.
	program.Send(tea.KeyMsg{Type: tea.KeyEsc})
	returnedBeforeSwitch := awaitTerminalSnapshot(t, h.updates, "manager cancel before switch", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenConversation && s.selection.PersonaID == "samantha" && s.voice
	})
	if returnedBeforeSwitch.sessionID != selectionBefore.SessionID || returnedBeforeSwitch.selection.ConversationEpoch != selectionBefore.ConversationEpoch {
		t.Fatalf("manager cancel before switch changed session identity: before=%+v after=%+v", selectionBefore, returnedBeforeSwitch)
	}
	if got := awaitPauseProof(t, runner.voiceStarts, "manager cancel before switch resume"); got != 5 {
		t.Fatalf("manager cancel before switch resumed voice call = %d, want 5", got)
	}
	// Reopen while listening so the actual switch case still proves
	// cancellation after the cancel-only cycle.
	sendTerminalEnter(program, "/personas")
	manager = awaitTerminalSnapshot(t, h.updates, "personas manager before switch", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenPersonas && s.selection.PersonaID == "samantha"
	})
	if manager.voice {
		t.Fatal("reopened manager left voice input enabled")
	}
	if got := awaitPauseProof(t, runner.voiceEnds, "manager reopening cancellation"); got != 5 {
		t.Fatalf("manager reopening canceled voice call = %d, want 5", got)
	}

	// Move the real manager cursor and select Santa. The selection remains in
	// the same session/epoch and does not turn the microphone back on.
	program.Send(tea.KeyMsg{Type: tea.KeyDown})
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	selected := awaitTerminalSnapshot(t, h.updates, "Santa selection", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenPersonas && s.selection.PersonaID == "santa"
	})
	if selected.selection.WorkDir != root {
		t.Fatalf("default persona inherited the pinned camp: %q, want %q", selected.selection.WorkDir, root)
	}
	if selected.voice || selected.selection.SessionID != selectionBefore.SessionID || selected.selection.ConversationEpoch != selectionBefore.ConversationEpoch || selected.agent != "Santa" {
		t.Fatalf("Santa selection changed session or mic state: before=%+v after=%+v", selectionBefore, selected)
	}

	program.Send(tea.KeyMsg{Type: tea.KeyEsc})
	returned := awaitTerminalSnapshot(t, h.updates, "manager cancel", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenConversation && s.selection.PersonaID == "santa" && s.voice
	})
	if returned.sessionID != selectionBefore.SessionID || returned.selection.ConversationEpoch != selectionBefore.ConversationEpoch {
		t.Fatalf("manager cancel changed session identity: before=%+v after=%+v", selectionBefore, returned)
	}
	if got := awaitPauseProof(t, runner.voiceStarts, "manager cancel resume"); got != 6 {
		t.Fatalf("manager cancel resumed voice call = %d, want 6", got)
	}

	// A nameless continuation after the switch is admitted to Santa's real
	// controlled provider and persisted through the normal session Store.
	sendTerminalText(program, "continue the canonical context")
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	if got := awaitPauseProof(t, runner.voiceEnds, "nameless text barge-in"); got != 6 {
		t.Fatalf("nameless input canceled voice call = %d, want 6", got)
	}
	awaitPauseProof(t, runner.textStarts, "nameless text turn")
	awaitPauseProof(t, runner.textEnds, "nameless text completion")
	if got := runner.textInputs; len(got) != 1 || got[0] != "continue the canonical context" {
		t.Fatalf("controlled provider inputs = %v", got)
	}
	final := awaitTerminalSnapshot(t, h.updates, "Santa continuation", func(s terminalPersonaSnapshot) bool {
		return s.screen == screenConversation && strings.Contains(s.view, "● Santa") && strings.Contains(s.view, "Santa keeps the canonical context.")
	})
	if final.selection.PersonaID != "santa" || final.agent != "Santa" || final.sessionID != selectionBefore.SessionID || !strings.Contains(final.view, "Santa keeps the canonical context.") {
		t.Fatalf("final Santa continuation lost attribution/session: %+v", final)
	}
	renderedTranscript := strings.Join(final.transcript, "\n") + "\n" + final.view
	if !strings.Contains(renderedTranscript, "● Samantha") || !strings.Contains(renderedTranscript, "● Santa") {
		t.Fatalf("rendered transcript lost historical/current speaker labels: %s", renderedTranscript)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkDir != root || loaded.Directories == nil || loaded.Directories.Personas["samantha"].Dir != pinned || loaded.Directories.Personas["santa"].Dir != root {
		t.Fatalf("durable persona directories = %+v", loaded)
	}
	foundSamanthaSeed, foundSantaReply := false, false
	for _, turn := range loaded.Turns {
		if turn.Role != "assistant" {
			continue
		}
		if turn.ResponseID == "seed-samantha-response" && turn.PersonaID == "samantha" && turn.PersonaDisplayName == "Samantha" {
			foundSamanthaSeed = true
		}
		if turn.PersonaID == "santa" && turn.PersonaDisplayName == "Santa" && turn.ResponseID != "" {
			foundSantaReply = true
		}
	}
	if loaded.SelectedPersonaID != "santa" || len(loaded.Turns) < 4 || !foundSamanthaSeed || !foundSantaReply {
		t.Fatalf("durable terminal session = %+v", loaded)
	}
	// handleTextDone resumes listening by returning a command that increments
	// the shared wait group before Bubble Tea executes it. Wait for that final
	// command to enter the runner so cleanup cannot observe a queued command
	// whose wait-group slot has no goroutine to release it.
	if got := awaitPauseProof(t, runner.voiceStarts, "post-turn listening"); got != 7 {
		t.Fatalf("post-turn voice call = %d, want 7", got)
	}
}
