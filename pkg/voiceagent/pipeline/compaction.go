package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// CompactConversation shrinks the model's working context while keeping the
// gist, identically for every provider. One summarize turn runs first — the
// provider sees its own full context (harness CLI transcript or messages[]),
// so the summary is grounded in what the model actually knows, not a local
// mirror. LoadHistory then performs the kind-appropriate reset+seed: harness
// providers drop the resume id and carry the seed in via the first-turn
// flatten; ollama rebuilds messages[]. The seed keeps the last exchange
// verbatim after the summary so the next turn continues naturally.
//
// Callers must own the pipeline (no turn in flight) — the TUI gates this the
// same way it gates text turns.
// maxCompactTail bounds the turns kept after the summary. With the summary
// itself that is 5, so the next turn still fits the providers' ≤6-turn flatten
// window.
const maxCompactTail = 4

// compactTail picks the turns that follow the summary in the reseeded history:
// the exchange the conversation is in the middle of, found by walking back to
// the last user turn rather than blindly taking the last two. An ollama tool
// loop ends on tool/assistant turns, so a fixed pair can miss the user turn
// that started the exchange entirely.
//
// Keep complete native tool groups, or drop the entire exchange when it
// exceeds the view budget. Old sessions without call metadata retain ordinary
// text, but their unpaired tool records cannot be sent back to the provider.
func compactTail(before []brain.Turn) []brain.Turn {
	start := -1
	for i := len(before) - 1; i >= 0; i-- {
		if before[i].Role == "user" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	tail := make([]brain.Turn, 0, len(before)-start)
	paired := false
	for _, t := range before[start:] {
		if t.Role == "tool" && !paired {
			continue
		}
		if t.Role != "tool" {
			paired = len(t.ToolCalls) > 0
		}
		if strings.TrimSpace(t.Content) == "" && len(t.ToolCalls) == 0 && t.Utterance == nil {
			continue
		}
		tail = append(tail, t)
	}
	return brain.HistoryWindow(tail, maxCompactTail)
}

func (p *Pipeline) autoCompactIfNeeded(ctx context.Context) error {
	return p.prepareTurn(ctx, "", nil, nil)
}

// prepareTurn checks both incoming prompt size and the historical count. All
// prepare work is bounded by the normal brain timeout, including filler speech.
func (p *Pipeline) prepareTurn(ctx context.Context, input string, metrics *turnMetrics, turn *turnConductor) error {
	if p.Brain == nil || p.CompactPrompt == "" {
		return nil
	}
	history := p.Brain.History()
	if view, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		history = view.ContextHistory()
	}
	compact := p.AutoCompactAfter > 0 && len(history) > p.AutoCompactAfter
	if budget, ok := p.Brain.(interface{ NeedsContinuityCompact(string) bool }); ok {
		compact = compact || budget.NeedsContinuityCompact(input)
	}
	if !compact {
		return nil
	}
	ctx, cancel := p.withBrainTimeout(ctx)
	defer cancel()
	p.emit(events.Info{Message: "compact: starting"})
	if filler, ok := p.Brain.(interface{ ContinuityCompactFiller() bool }); ok && filler.ContinuityCompactFiller() && metrics != nil && turn != nil {
		p.speakBestEffort(ctx, "Give me a moment to gather my notes.", metrics, turn)
		turn.to(TurnThinking)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.compactConversation(ctx, input, true)
}

func (p *Pipeline) CompactConversation(ctx context.Context) error {
	return p.compactConversation(ctx, "", false)
}

func (p *Pipeline) compactConversation(ctx context.Context, input string, automatic bool) (resultErr error) {
	if p.Brain == nil || p.CompactPrompt == "" {
		return errors.New("compact prompt not configured")
	}
	if err := p.awaitProvider(ctx); err != nil {
		return err
	}
	history := p.Brain.History()
	turnsBefore := len(history)
	if turnsBefore < 2 {
		return errors.New("nothing to compact yet")
	}
	started := time.Now()
	terminal := "failed"
	stage := func(name string) { p.emit(events.CompactionProgress{Stage: name, Elapsed: time.Since(started)}) }
	defer func() {
		if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
			terminal = "canceled"
		}
		stage(terminal)
	}()
	working := history
	if view, ok := p.Brain.(brain.ContextHistoryProvider); ok {
		working = view.ContextHistory()
	}
	if err := brain.ValidateTextProjection(working); err != nil {
		return err
	}
	before := append([]brain.Turn(nil), history...)
	tailSource := before
	if brain.HostedSharedHistory(p.Brain) {
		// It sends only its persona's share; the seed must not add to it.
		tailSource = working
	} else if canonical, ok := p.Brain.(brain.CanonicalHistoryProvider); ok {
		tailSource = canonical.CanonicalHistory()
	}
	tail := compactTail(tailSource)
	if p.OnCompactBackup != nil {
		p.OnCompactBackup(before)
	}
	stage("trim")
	if prep, ok := p.Brain.(interface{ PrepareCompact(string) }); ok {
		prep.PrepareCompact(p.compactionInstruction())
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if automatic {
		working := p.Brain.History()
		if view, ok := p.Brain.(brain.ContextHistoryProvider); ok {
			working = view.ContextHistory()
		}
		pressure := false
		if budget, ok := p.Brain.(interface{ NeedsContinuityCompact(string) bool }); ok {
			pressure = budget.NeedsContinuityCompact(input)
		}
		if !pressure && (p.AutoCompactAfter <= 0 || len(working) <= p.AutoCompactAfter) {
			terminal = "complete"
			p.emit(events.Info{Message: "compact: trimmed"})
			return nil
		}
	}
	if err := p.announceLiveHosted(ctx); err != nil {
		return err
	}
	ctx, cancel := p.withBrainTimeout(ctx)
	defer cancel()
	// The dense briefing replaces voice instructions. Tools and durable-history
	// writes stay off; only a successful summary can reseed the working context.
	stage("summarize")
	opts := brain.StreamOptions{OmitTurnInstruction: true, TransientHistory: true}
	// A provider keeping a persona session summarizes the lines it has not
	// seen as well; its next session starts from the summary and only the
	// lines after it.
	var summarized func()
	if t, ok := p.transcriptFor(p.Brain).(SessionTranscript); ok && brain.KeepsPersonaSession(p.Brain) {
		if id, catchUp, covered, ok := t.LiveCatchUp(); ok {
			epoch := p.ConversationScope().ConversationEpoch
			opts.CatchUp = &catchUp
			summarized = func() { _ = t.AppendSeen(epoch, id, covered) }
		}
	}
	summary, err := p.Brain.ThinkFull(ctx, p.compactionInstruction(), opts)
	if err == nil && strings.TrimSpace(summary) == "" {
		err = errors.New("empty summary")
	}
	if err != nil {
		if automatic && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			if trim, ok := p.Brain.(interface{ TrimContinuity(string) }); ok {
				stage("trim")
				trim.TrimContinuity(input)
			}
			terminal = "skipped"
			p.emit(events.Info{Message: "compact: skipped (" + err.Error() + ")"})
			return nil
		}
		return fmt.Errorf("compact summarize turn: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage("reseed")
	seed := append([]brain.Turn{{Role: "assistant", Content: summary}}, tail...)
	if reseed, ok := p.Brain.(interface{ ReseedContinuity([]brain.Turn) }); ok {
		reseed.ReseedContinuity(seed)
	} else {
		p.Brain.LoadHistory(seed)
	}
	if summarized != nil {
		summarized()
	}
	if automatic {
		if budget, ok := p.Brain.(interface{ NeedsContinuityCompact(string) bool }); ok && budget.NeedsContinuityCompact(input) {
			if trim, ok := p.Brain.(interface{ TrimContinuity(string) }); ok {
				trim.TrimContinuity(input)
			}
		}
	}
	terminal = "complete"
	p.emit(events.ConversationCompacted{TurnsBefore: turnsBefore, Summary: summary})
	if p.OnTurn != nil {
		p.OnTurn()
	}
	return nil
}

// Attribution is part of the shared conversation contract even when a user
// customizes the compact document. Keep these instructions out of ordinary
// legacy compaction where no historical identities are known.
func (p *Pipeline) compactionInstruction() string {
	var turns []brain.Turn
	if canonical, ok := p.Brain.(brain.CanonicalHistoryProvider); ok {
		turns = canonical.CanonicalHistory()
	} else if t := p.transcriptFor(p.Brain); t != nil {
		turns = t.Turns()
	}
	for _, turn := range turns {
		if turn.PersonaID != "" || turn.PersonaDisplayName != "" {
			return p.CompactPrompt + "\n\nHistorical attribution requirements: This is a continuity summary of a shared conversation, not a new reply by the current persona. Preserve the historical persona name for each retained fact or decision from an assistant. Keep user-provided facts attributed to the user. Preserve different personas separately, including their disagreements. Do not imitate historical assistant labels in the summary or present internal metadata as user-facing text."
		}
	}
	return p.CompactPrompt
}
