package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// compactBrain is a stateful fake: ThinkFull records the instruction and
// appends the turn to history the way real providers do, so the test can
// verify the snapshot/tail semantics of CompactConversation.
type compactBrain struct {
	history     []brain.Turn
	summary     string
	fullErr     error
	thinkInputs []string
	thinkOpts   []brain.StreamOptions
	loaded      [][]brain.Turn
}

func (b *compactBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("not used")
}

// ThinkFull models the real provider contract: the input is appended before the
// model call (the prompt is built from history) and rolled back if the call
// fails. A fake that skips the append on the error path cannot observe a
// dangling compact turn, which is what made the failure test a false green.
func (b *compactBrain) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	b.thinkInputs = append(b.thinkInputs, input)
	b.thinkOpts = append(b.thinkOpts, opts)

	restore := len(b.history)
	b.history = append(b.history, brain.Turn{Role: "user", Content: input})

	err := b.fullErr
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		b.history = b.history[:restore]
		return "", err
	}

	b.history = append(b.history, brain.Turn{Role: "samantha", Content: b.summary})
	return b.summary, nil
}

func (b *compactBrain) ClearHistory()         {}
func (b *compactBrain) History() []brain.Turn { return b.history }
func (b *compactBrain) LoadHistory(turns []brain.Turn) {
	b.history = turns
	b.loaded = append(b.loaded, turns)
}

func assertTurns(t *testing.T, got, want []brain.Turn, context string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %+v, want %+v", context, got, want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("%s[%d] = %+v, want %+v", context, i, got[i], want[i])
		}
	}
}

func compactHistory() []brain.Turn {
	return []brain.Turn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply one"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "reply two"},
	}
}

func TestAutoCompactIfNeededSummarizesInsteadOfDropping(t *testing.T) {
	hist := make([]brain.Turn, 0, 22)
	for i := 0; i < 11; i++ {
		hist = append(hist,
			brain.Turn{Role: "user", Content: "turn"},
			brain.Turn{Role: "assistant", Content: "reply"},
		)
	}
	b := &compactBrain{history: hist, summary: "the gist"}
	var compacted int
	bus := events.NewBus()
	bus.SubscribeAll(func(e events.Event) {
		if _, ok := e.(events.ConversationCompacted); ok {
			compacted++
		}
	})
	p := &Pipeline{Brain: b, Events: bus, CompactPrompt: "SUMMARIZE", AutoCompactAfter: 20}
	if err := p.autoCompactIfNeeded(context.Background()); err != nil {
		t.Fatalf("autoCompactIfNeeded: %v", err)
	}
	if compacted != 1 {
		t.Fatalf("compacted = %d, want 1", compacted)
	}
	if len(b.history) >= len(hist) {
		t.Fatalf("history still %d, want a compacted seed", len(b.history))
	}
	if b.history[0].Content != "the gist" {
		t.Fatalf("seed[0] = %+v, want summary first", b.history[0])
	}
	p2 := &Pipeline{Brain: &compactBrain{history: compactHistory()}, CompactPrompt: "S", AutoCompactAfter: 20}
	if err := p2.autoCompactIfNeeded(context.Background()); err != nil {
		t.Fatalf("under threshold: %v", err)
	}
	if len(p2.Brain.History()) != 4 {
		t.Fatalf("under threshold must not compact, got %d", len(p2.Brain.History()))
	}
}

func TestCompactConversationSummarizesAndReseeds(t *testing.T) {
	b := &compactBrain{history: compactHistory(), summary: "the summary"}
	bus := events.NewBus()
	var compacted []events.ConversationCompacted
	bus.SubscribeAll(func(e events.Event) {
		if c, ok := e.(events.ConversationCompacted); ok {
			compacted = append(compacted, c)
		}
	})

	var backup []brain.Turn
	saves := 0
	p := &Pipeline{
		Brain:           b,
		Events:          bus,
		CompactPrompt:   "SUMMARIZE THE CONVERSATION",
		OnCompactBackup: func(turns []brain.Turn) { backup = turns },
		OnTurn:          func() { saves++ },
	}

	if err := p.CompactConversation(context.Background()); err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}

	if len(b.thinkInputs) != 1 || b.thinkInputs[0] != "SUMMARIZE THE CONVERSATION" {
		t.Fatalf("summarize turn inputs = %v, want the compact prompt once", b.thinkInputs)
	}
	// The briefing has to be the model's last instruction: the per-turn voice
	// instruction ("2–3 sentences max") is appended after it otherwise.
	if !b.thinkOpts[0].OmitTurnInstruction {
		t.Fatal("summarize turn must set OmitTurnInstruction")
	}
	if b.thinkOpts[0].ToolsEnabled {
		t.Fatal("summarize turn must not enable tools")
	}
	// Seed: summary first, then the last exchange verbatim (from the
	// pre-summarize snapshot, not the mutated history).
	want := []brain.Turn{
		{Role: "assistant", Content: "the summary"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "reply two"},
	}
	if len(b.history) != len(want) {
		t.Fatalf("reseeded history = %+v, want %+v", b.history, want)
	}
	for i := range want {
		if !reflect.DeepEqual(b.history[i], want[i]) {
			t.Fatalf("seed[%d] = %+v, want %+v", i, b.history[i], want[i])
		}
	}
	// Backup got the pre-compact turns, untouched by the summarize turn.
	if len(backup) != 4 || backup[3].Content != "reply two" {
		t.Fatalf("backup = %+v, want the 4 original turns", backup)
	}
	if len(compacted) != 1 || compacted[0].TurnsBefore != 4 || compacted[0].Summary != "the summary" {
		t.Fatalf("events = %+v, want one ConversationCompacted{TurnsBefore:4}", compacted)
	}
	if saves != 1 {
		t.Fatalf("OnTurn calls = %d, want 1 (persist the compacted session)", saves)
	}
}

// An ollama tool loop leaves history ending on tool/assistant turns. Seeding a
// blind last-two slice then strands a tool result — brain.Turn has no tool-call
// id, so the assistant message that requested it can never come along — and
// loses the user turn that started the exchange.
func TestCompactConversationSeedsPastToolTurns(t *testing.T) {
	toolTail := []brain.Turn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "reply one"},
		{Role: "user", Content: "what is in that file"},
		{Role: "assistant", Content: ""}, // tool-call-only message
		{Role: "tool", Content: "file contents"},
		{Role: "assistant", Content: "it holds the config"},
	}
	b := &compactBrain{history: toolTail, summary: "the summary"}
	p := &Pipeline{Brain: b, CompactPrompt: "S"}

	if err := p.CompactConversation(context.Background()); err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}
	assertTurns(t, b.history, []brain.Turn{
		{Role: "assistant", Content: "the summary"},
		{Role: "user", Content: "what is in that file"},
		{Role: "assistant", Content: "it holds the config"},
	}, "seed after a tool-loop tail")
}

type projectedCompactBrain struct {
	compactBrain
	canonical []brain.Turn
	projected []brain.Turn
}

func (b *projectedCompactBrain) ContextHistory() []brain.Turn { return b.projected }
func (b *projectedCompactBrain) CanonicalHistory() []brain.Turn {
	return b.canonical
}

func TestAutoCompactAfterPersonaSwitchKeepsCanonicalUserAndForeignToolExchange(t *testing.T) {
	call := brain.TurnToolCall{ID: "lookup-1", Name: "lookup", Arguments: `{}`}
	canonical := []brain.Turn{
		{Role: "user", Content: "remember the cobalt fact"},
		{Role: "assistant", Content: "I will look that up.", PersonaID: "santa", PersonaDisplayName: "Santa", ToolCalls: []brain.TurnToolCall{call}},
		{Role: "tool", Content: "cobalt is in the fixture", ToolCallID: "lookup-1", ToolName: "lookup", PersonaID: "santa", PersonaDisplayName: "Santa"},
		{Role: "assistant", Content: "The fixture contains cobalt.", PersonaID: "santa", PersonaDisplayName: "Santa"},
	}
	b := &projectedCompactBrain{
		compactBrain: compactBrain{history: append([]brain.Turn(nil), canonical...), summary: "lossy summary"},
		canonical:    canonical,
		projected: []brain.Turn{
			{Role: "user", Content: "remember the cobalt fact"},
			{Role: "user", Content: "Shared conversation — Santa said:\nI will look that up."},
			{Role: "shared_tool", Content: "Shared tool result (lookup) from Santa:\ncobalt is in the fixture"},
			{Role: "user", Content: "Shared conversation — Santa said:\nThe fixture contains cobalt."},
		},
	}
	p := &Pipeline{Brain: b, CompactPrompt: "SUMMARIZE", AutoCompactAfter: 3}

	// This is the first turn after switching persona: compaction runs before
	// the new persona's next user input is appended.
	if err := p.autoCompactIfNeeded(context.Background()); err != nil {
		t.Fatalf("autoCompactIfNeeded: %v", err)
	}

	assertTurns(t, b.history, append([]brain.Turn{{Role: "assistant", Content: "lossy summary"}}, canonical...), "reseeded canonical tail")
}

func TestCompactTailBounds(t *testing.T) {
	// No user turn within the bound: the tail is capped instead of walking the
	// whole transcript back, so the seed still fits the ≤6-turn flatten window.
	var assistantOnly []brain.Turn
	for range 8 {
		assistantOnly = append(assistantOnly, brain.Turn{Role: "assistant", Content: "reply"})
	}
	if got := compactTail(assistantOnly); len(got) != 0 {
		t.Fatalf("tail without a user boundary = %+v, want summary-only reseed", got)
	}

	// Everything filtered out is an empty tail, not a panic: the summary alone
	// reseeds the conversation.
	if got := compactTail([]brain.Turn{{Role: "tool", Content: "x"}, {Role: "assistant", Content: " "}}); len(got) != 0 {
		t.Fatalf("tail = %+v, want empty", got)
	}
}

func TestCompactConversationGuards(t *testing.T) {
	t.Run("no prompt configured", func(t *testing.T) {
		p := &Pipeline{Brain: &compactBrain{history: compactHistory()}}
		if err := p.CompactConversation(context.Background()); err == nil {
			t.Fatal("want error when CompactPrompt is empty")
		}
	})

	t.Run("nothing to compact", func(t *testing.T) {
		p := &Pipeline{Brain: &compactBrain{}, CompactPrompt: "S"}
		if err := p.CompactConversation(context.Background()); err == nil {
			t.Fatal("want error on empty history")
		}
	})

	t.Run("summarize failure leaves history intact", func(t *testing.T) {
		b := &compactBrain{history: compactHistory(), fullErr: errors.New("model down")}
		saves := 0
		p := &Pipeline{Brain: b, CompactPrompt: "S", OnTurn: func() { saves++ }}
		err := p.CompactConversation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "model down") {
			t.Fatalf("error = %v, want wrapped summarize failure", err)
		}
		if len(b.loaded) != 0 {
			t.Fatal("LoadHistory must not run when the summarize turn fails")
		}
		if saves != 0 {
			t.Fatal("OnTurn must not run on failure")
		}
		// The conversation must be exactly what it was: no compact briefing
		// left parked as a user turn, and no reseed.
		assertTurns(t, b.history, compactHistory(), "history after a failed summarize")
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b := &compactBrain{history: compactHistory(), summary: "s"}
		p := &Pipeline{Brain: b, CompactPrompt: "S"}
		if err := p.CompactConversation(ctx); err == nil {
			t.Fatal("want error from canceled context")
		}
		if len(b.loaded) != 0 {
			t.Fatal("LoadHistory must not run after cancellation")
		}
		assertTurns(t, b.history, compactHistory(), "history after cancellation")
	})
}

// continuityCompactBrain isolates the provider's token-budget seam from the
// older count trigger, and records whether the real turn was sent after failure.
type continuityCompactBrain struct {
	compactBrain
	receivedInput string
	streamCalls   int
}

func (b *continuityCompactBrain) NeedsContinuityCompact(input string) bool {
	b.receivedInput = input
	return true
}
func (b *continuityCompactBrain) ContinuityCompactFiller() bool { return true }
func (b *continuityCompactBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.streamCalls++
	out := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		out <- "ok"
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func TestPrepareTurnTokenPressureSpeaksOnceBeforeCompact(t *testing.T) {
	b := &continuityCompactBrain{compactBrain: compactBrain{history: compactHistory(), summary: "WI-b94dad is the pending task."}}
	tts := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Brain: b, TTS: tts, Player: player, CompactPrompt: "SUMMARIZE", AutoCompactAfter: 100}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	turn.to(TurnThinking)
	if err := p.prepareTurn(t.Context(), "remember WI-b94dad", metrics, turn); err != nil {
		t.Fatal(err)
	}
	if b.receivedInput != "remember WI-b94dad" || len(b.thinkInputs) != 1 {
		t.Fatalf("budget input/compact missing: %+v", b)
	}
	if got := tts.Texts(); len(got) != 1 || got[0] != "Give me a moment to gather my notes." {
		t.Fatalf("filler=%q", got)
	}
	if len(b.history) == 0 || !strings.Contains(b.history[0].Content, "WI-b94dad") {
		t.Fatal("compacted identity missing")
	}
}

func TestPrepareFailureDoesNotSpeakRecoveryReply(t *testing.T) {
	b := &continuityCompactBrain{compactBrain: compactBrain{history: compactHistory(), fullErr: errors.New("summary unavailable")}}
	var ready events.ResponseReady
	var infos []string
	bus := events.NewBus()
	events.Subscribe(bus, func(e events.ResponseReady) { ready = e })
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Brain: b, CompactPrompt: "SUMMARIZE", Events: bus, AutoCompactAfter: 100}
	if err := p.RunTurnTextMode(t.Context(), "follow up"); err != nil {
		t.Fatal(err)
	}
	if ready.Degraded || strings.Contains(ready.Response, brain.RecoveryReply) {
		t.Fatalf("compact failure must not RecoveryReply: %+v", ready)
	}
	if b.streamCalls == 0 {
		t.Fatal("user turn should still run after compact skip")
	}
	found := false
	for _, m := range infos {
		if strings.HasPrefix(m, "compact: skipped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing compact skip info: %v", infos)
	}
}
