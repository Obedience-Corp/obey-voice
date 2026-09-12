package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// pauseTerminalBrain is a controlled provider with the same history and
// persona projection capabilities required by personaruntime.Selector. It
// lets this proof exercise the production pipeline without a model, device, or
// network while retaining the real response envelope and session persistence.
type pauseTerminalBrain struct {
	mu         sync.Mutex
	history    []brain.Turn
	promptHash string
	closed     bool
}

func (b *pauseTerminalBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
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
	answer := fmt.Sprintf("%s keeps the canonical context.", identity.PersonaDisplayName)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("pause proof brain is closed")
	}
	b.history = append(b.history,
		brain.Turn{Role: "user", Content: original, RoutedInput: input},
		brain.Turn{Role: "assistant", Content: answer, PersonaID: identity.PersonaID, PersonaDisplayName: identity.PersonaDisplayName, PromptHash: identity.PromptHash, ResponseID: identity.ResponseID},
	)
	b.mu.Unlock()
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- answer
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (b *pauseTerminalBrain) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	stream, err := b.ThinkStream(ctx, input, opts)
	if err != nil {
		return "", err
	}
	var response string
	for chunk := range stream.Chunks {
		response += chunk
	}
	if result := <-stream.Done; result.Err != nil {
		return "", result.Err
	}
	return response, nil
}

func (b *pauseTerminalBrain) ClearHistory() {
	b.mu.Lock()
	b.history = nil
	b.mu.Unlock()
}

func (b *pauseTerminalBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]brain.Turn(nil), b.history...)
}

func (b *pauseTerminalBrain) LoadHistory(turns []brain.Turn) { _ = b.ImportHistory(turns) }

func (b *pauseTerminalBrain) ImportHistory(turns []brain.Turn) error {
	b.mu.Lock()
	b.history = append([]brain.Turn(nil), turns...)
	b.mu.Unlock()
	return nil
}

func (b *pauseTerminalBrain) FreezePersonaPrompt() string { return b.promptHash }

func (b *pauseTerminalBrain) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

// pauseTerminalRunner provides a cancellable listening turn, then delegates
// typed input to the real Pipeline. Its ordered log proves that a text turn
// cannot start until the canceled listening command has returned.
type pauseTerminalRunner struct {
	pipeline *pipeline.Pipeline

	mu          sync.Mutex
	voiceCalls  int
	textInputs  []string
	ordered     []string
	voiceStarts chan int
	voiceEnds   chan int
	textStarts  chan struct{}
	textEnds    chan struct{}
}

func (r *pauseTerminalRunner) RunTurn(ctx context.Context) (string, error) {
	r.mu.Lock()
	r.voiceCalls++
	n := r.voiceCalls
	r.ordered = append(r.ordered, fmt.Sprintf("voice-start-%d", n))
	r.mu.Unlock()
	r.voiceStarts <- n
	<-ctx.Done()
	r.mu.Lock()
	r.ordered = append(r.ordered, fmt.Sprintf("voice-end-%d", n))
	r.mu.Unlock()
	r.voiceEnds <- n
	return "", ctx.Err()
}

func (r *pauseTerminalRunner) RunTurnTextMode(ctx context.Context, input string) error {
	r.mu.Lock()
	r.textInputs = append(r.textInputs, input)
	r.ordered = append(r.ordered, "text-start")
	r.mu.Unlock()
	select {
	case r.textStarts <- struct{}{}:
	default:
	}
	err := r.pipeline.RunTurnTextMode(ctx, input)
	r.mu.Lock()
	r.ordered = append(r.ordered, "text-end")
	r.mu.Unlock()
	select {
	case r.textEnds <- struct{}{}:
	default:
	}
	return err
}

func (r *pauseTerminalRunner) StopPlayback() {}

func (r *pauseTerminalRunner) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ordered...)
}

type pauseTerminalReadyMsg struct{}
type pauseTerminalInitMsg struct{}

type pauseTerminalHarness struct {
	model     conversationModel
	runtime   *ConversationRuntime
	runner    *pauseTerminalRunner
	ctx       context.Context
	wg        *sync.WaitGroup
	ready     chan struct{}
	paused    chan string
	selected  chan string
	resumed   chan string
	responses chan events.ResponseReady
}

func (h *pauseTerminalHarness) Init() tea.Cmd {
	return func() tea.Msg { return pauseTerminalInitMsg{} }
}

func (h *pauseTerminalHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(pauseTerminalInitMsg); ok {
		cmd := h.model.startConversation(conversationDeps{runner: h.runner, bus: h.runtime.Bus, voice: true, ctx: h.ctx, wg: h.wg, sessionID: h.runtime.SessionID})
		h.model.configureSelection(h.runtime)
		return h, tea.Batch(cmd, func() tea.Msg { return pauseTerminalReadyMsg{} })
	}
	var cmd tea.Cmd
	h.model, cmd = h.model.Update(msg)
	switch msg := msg.(type) {
	case pauseTerminalReadyMsg:
		select {
		case <-h.ready:
		default:
			close(h.ready)
		}
	case voiceTurnDoneMsg:
		if !h.model.voiceEnabled && h.model.turnState == turnIdle {
			h.paused <- stripANSI(h.model.View())
		}
	case tea.KeyMsg:
		if msg.String() == "ctrl+g" && h.model.voiceEnabled {
			h.resumed <- stripANSI(h.model.View())
		}
	case personaSelectionRequestMsg:
		return h, h.model.enqueuePersonaSelection(msg.id)
	case personaSelectionResultMsg:
		accepted, next := h.model.finishPersonaSelection(msg)
		if accepted && msg.err == nil {
			h.model.applySelectionSnapshot(msg.selection)
			h.model.cfg = msg.cfg
			if msg.selection.PersonaID == "santa" {
				h.selected <- stripANSI(h.model.View())
			}
		}
		if next != nil {
			cmd = tea.Batch(cmd, next)
		}
	case busEventMsg:
		if response, ok := msg.event.(events.ResponseReady); ok && response.Envelope != nil {
			h.responses <- response
		}
	}
	return h, cmd
}

func (h *pauseTerminalHarness) View() string { return h.model.View() }

func pauseProofProfile(id, display string) persona.Profile {
	return persona.Profile{
		Schema: persona.Schema, ID: id, DisplayName: display,
		Brain:   persona.Brain{Provider: "ollama", Model: id + "-fixture"},
		TTS:     persona.TTS{Provider: "kokoro", Voice: id + "-voice"},
		Prompts: persona.PromptRefs{Persona: id},
	}
}

func writePauseProofCatalog(t *testing.T, root string, profile persona.Profile) {
	t.Helper()
	dir := filepath.Join(root, "personas", profile.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "persona.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	promptDir := filepath.Join(root, "prompts", string(prompts.KindPersona))
	if err := os.MkdirAll(promptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := prompts.Document{Schema: prompts.Schema, Prompt: prompts.Prompt{Name: profile.ID, Kind: prompts.KindPersona, SystemPrompt: prompts.SystemPrompt{Identity: "You are " + profile.DisplayName + "."}}}
	data, err = yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, profile.ID+".yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func awaitPauseProof[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}

// TestPersonaPauseResumeTerminalProof runs the owning Bubble Tea control path:
// pause Ctrl+G, in-chat persona selection through the real Selector, resume
// Ctrl+G, then typed input after the listening cancellation has joined.
func TestPersonaPauseResumeTerminalProof(t *testing.T) {
	root := t.TempDir()
	profiles := []persona.Profile{pauseProofProfile("samantha", "Samantha"), pauseProofProfile("santa", "Santa")}
	for _, profile := range profiles {
		writePauseProofCatalog(t, root, profile)
	}
	base := &config.Config{ActivePersona: "samantha", AgentName: "Samantha", Persona: "samantha", BrainProvider: "ollama", OllamaModel: "samantha-fixture", PromptsDir: filepath.Join(root, "prompts"), WorkDir: root, VoiceWorkEnabled: true, VoiceWorkerProvider: "stub"}
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	initial, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	old := &pauseTerminalBrain{promptHash: "samantha-initial", history: []brain.Turn{{Role: "user", Content: "The canonical context is blue comet."}, {Role: "assistant", Content: "Samantha stored blue comet."}}}
	p := &pipeline.Pipeline{Brain: old, Events: events.NewBus(), VoiceToolsEnabled: false}
	jobs := job.NewTable(t.Context())
	agency := pipeline.NewAgency(p, jobs, nil, base, nil)
	agency.Wire()
	if err := p.EnableConversationWork("pause-session", nil, base); err != nil {
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
			return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
		},
		NewBrain:  factory,
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
	p.OnTurn = func() {
		if err := store.Save(sess, p.Brain.History()); err != nil {
			t.Errorf("save turn: %v", err)
		}
	}
	selectionBefore := selector.Snapshot()
	if selectionBefore.SessionID != sess.ID || selectionBefore.ConversationEpoch == "" {
		t.Fatalf("initial selector identity = %+v", selectionBefore)
	}
	runner := &pauseTerminalRunner{pipeline: p, voiceStarts: make(chan int, 8), voiceEnds: make(chan int, 8), textStarts: make(chan struct{}, 1), textEnds: make(chan struct{}, 1)}
	rt := &ConversationRuntime{
		Pipeline: p, Bus: p.Events, Voice: true, PersonaID: initial.PersonaID, AgentName: initial.DisplayName,
		SessionCfg: initial.Config(), SessionID: sess.ID,
		SelectPersona: func(ctx context.Context, id string) (events.PersonaSelection, *config.Config, error) {
			selected, err := selector.Select(ctx, id, "ui")
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
	model := newConversation(initial.DisplayName)
	model.setSize(100, 30)
	h := &pauseTerminalHarness{model: model, runtime: rt, runner: runner, ctx: ctx, wg: wg, ready: make(chan struct{}), paused: make(chan string, 1), selected: make(chan string, 1), resumed: make(chan string, 1), responses: make(chan events.ResponseReady, 1)}
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
	})
	awaitPauseProof(t, h.ready, "terminal startup")
	if got := awaitPauseProof(t, runner.voiceStarts, "initial listening"); got != 1 {
		t.Fatalf("initial voice call = %d, want 1", got)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	awaitPauseProof(t, runner.voiceEnds, "paused listening cancellation")
	pausedView := awaitPauseProof(t, h.paused, "paused terminal state")
	if !strings.Contains(pausedView, "mic off") {
		t.Fatalf("pause view/state:\n%s", pausedView)
	}
	program.Send(personaSelectionRequestMsg{id: "santa"})
	selectedView := awaitPauseProof(t, h.selected, "Santa selection")
	selectionAfter := selector.Snapshot()
	if selectionAfter.PersonaID != "santa" || selectionAfter.SessionID != selectionBefore.SessionID || selectionAfter.ConversationEpoch != selectionBefore.ConversationEpoch {
		t.Fatalf("selection identity changed across pause: before=%+v after=%+v", selectionBefore, selectionAfter)
	}
	if !strings.Contains(selectedView, "Santa") {
		t.Fatalf("Santa selection was not rendered:\n%s", selectedView)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceStarts, "resumed listening"); got != 2 {
		t.Fatalf("resumed voice call = %d, want 2", got)
	}
	resumedView := awaitPauseProof(t, h.resumed, "resumed terminal state")
	if !strings.Contains(resumedView, "mic on") || !strings.Contains(resumedView, "Santa") {
		t.Fatalf("resume view missing mic on:\n%s", resumedView)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceEnds, "Santa pause listening cancellation"); got != 2 {
		t.Fatalf("Santa pause voice call = %d, want 2", got)
	}
	pausedSantaView := awaitPauseProof(t, h.paused, "Santa paused terminal state")
	if !strings.Contains(pausedSantaView, "mic off") || !strings.Contains(pausedSantaView, "Santa") {
		t.Fatalf("Santa pause view missing mic off or Santa:\n%s", pausedSantaView)
	}
	selectionPaused := selector.Snapshot()
	if selectionPaused.PersonaID != selectionAfter.PersonaID || selectionPaused.Revision != selectionAfter.Revision || selectionPaused.SessionID != selectionAfter.SessionID || selectionPaused.ConversationEpoch != selectionAfter.ConversationEpoch {
		t.Fatalf("selection changed during Santa pause: before=%+v after=%+v", selectionAfter, selectionPaused)
	}
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	if got := awaitPauseProof(t, runner.voiceStarts, "Santa resumed listening"); got != 3 {
		t.Fatalf("Santa resumed voice call = %d, want 3", got)
	}
	resumedSantaView := awaitPauseProof(t, h.resumed, "Santa resumed terminal state")
	if !strings.Contains(resumedSantaView, "mic on") || !strings.Contains(resumedSantaView, "Santa") {
		t.Fatalf("Santa resume view missing mic on or Santa:\n%s", resumedSantaView)
	}
	selectionResumed := selector.Snapshot()
	if selectionResumed.PersonaID != selectionPaused.PersonaID || selectionResumed.Revision != selectionPaused.Revision || selectionResumed.SessionID != selectionPaused.SessionID || selectionResumed.ConversationEpoch != selectionPaused.ConversationEpoch {
		t.Fatalf("selection changed during Santa resume: before=%+v after=%+v", selectionPaused, selectionResumed)
	}
	for _, r := range "what is the canonical context?" {
		program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	if got := awaitPauseProof(t, runner.voiceEnds, "typed-input listening cancellation"); got != 3 {
		t.Fatalf("typed input canceled voice call = %d, want 3", got)
	}
	awaitPauseProof(t, runner.textStarts, "typed turn after listening drain")
	response := awaitPauseProof(t, h.responses, "Santa response envelope")
	awaitPauseProof(t, runner.textEnds, "typed turn persistence")
	if response.Envelope.PersonaID != "santa" || response.Envelope.SessionID != selectionBefore.SessionID || response.Envelope.ConversationEpoch != selectionBefore.ConversationEpoch {
		t.Fatalf("nameless response envelope = %+v; selection before=%+v", response.Envelope, selectionBefore)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "santa" || len(loaded.Turns) < 4 || loaded.Turns[0].Content != "The canonical context is blue comet." {
		t.Fatalf("persisted Santa/history state = %+v", loaded)
	}
	ordered := runner.order()
	voiceEnd, textStart := -1, -1
	for i, entry := range ordered {
		if entry == "voice-end-3" {
			voiceEnd = i
		}
		if entry == "text-start" {
			textStart = i
		}
	}
	if voiceEnd < 0 || textStart < 0 || voiceEnd > textStart {
		t.Fatalf("typed turn started before listening drained: %v", ordered)
	}
	// The completed text turn schedules the next listening command and adds
	// its wait-group slot before Bubble Tea executes it. Observe that command
	// entering the runner before cleanup can cancel and quit the program; this
	// avoids a queued command leaving an unreleased wait-group slot.
	if got := awaitPauseProof(t, runner.voiceStarts, "post-turn listening"); got != 4 {
		t.Fatalf("post-turn voice call = %d, want 4", got)
	}
}
