package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// ErrPromptBudget is the hard refuse when a Chat request would exceed 90% of
// num_ctx. Compact treats this as "trim and continue", not a user-facing
// RecoveryReply.
var ErrPromptBudget = errors.New("context exceeds safe prompt budget")

// AddContinuityNote is the projection boundary shared with worker briefs. Call
// only while owning the provider conversation (as with History/LoadHistory).
func (o *OllamaBrain) AddContinuityNote(raw string, p continuity.Provenance) string {
	note, entry := continuity.ProjectToolResult(raw, p)
	if o.cfg != nil && o.cfg.ContinuityArchiveEnabled {
		o.archive.Add(entry)
	}
	o.emitContinuity(events.Info{Message: fmt.Sprintf("continuity.projection tool=%s raw_bytes=%d projected_bytes=%d", entry.Provenance.ToolName, len(raw), len(note))})
	return note
}

// AddDelegationNote projects a worker brief with its job ID as provenance.
func (o *OllamaBrain) AddDelegationNote(brief, sourceRef string) string {
	return o.AddContinuityNote(brief, continuity.Provenance{ToolName: "agent_delegate", SourceRef: sourceRef})
}

func (o *OllamaBrain) projectedToolResult(tc api.ToolCall, raw string) api.Message {
	if o.cfg != nil && o.cfg.ContinuityProjectionEnabled {
		raw = o.AddContinuityNote(raw, continuity.Provenance{ToolName: tc.Function.Name})
	}
	// Preserve native tool role and call IDs: recent Ollama/Qwen requires paired
	// tool messages. A projection is untrusted tool data, not assistant authority.
	return toolResultMessage(tc, raw)
}

func (o *OllamaBrain) archiveMessages(messages []api.Message) {
	if o.cfg == nil || !o.cfg.ContinuityArchiveEnabled || len(messages) == 0 {
		return
	}
	var b strings.Builder
	for _, m := range messages {
		role := m.Role
		if role == ollamaSharedRole {
			role = "shared conversation"
		}
		if role == ollamaSharedToolRole {
			role = "shared tool result"
		}
		b.WriteString(role + ": " + continuity.Clip(m.Content, 512) + "\n")
		if b.Len() >= continuity.NoteBytes {
			break
		}
	}
	o.archive.Add(continuity.Entry{Kind: "turn_block", Summary: b.String()})
}

// ContinuityArchive exposes bounded session-local provenance for explicit
// follow-up integrations. It never becomes persisted session history.
func (o *OllamaBrain) ContinuityArchive() []continuity.Entry { return o.archive.Snapshot() }

// ContinuityNotes supplies recent summaries to compaction, never raw payloads.
func (o *OllamaBrain) ContinuityNotes() string {
	entries := o.archive.Snapshot()
	var b strings.Builder
	for i := len(entries) - 1; i >= 0; i-- {
		if b.Len()+len(entries[i].Summary) > continuity.NoteBytes {
			break
		}
		b.WriteString(entries[i].Summary + "\n")
	}
	return b.String()
}

func (o *OllamaBrain) effectiveNumCtx() int {
	return o.numCtxFor(o.contextHighWater)
}

// numCtxFor is effectiveNumCtx over a snapshot of the high-water mark, for a
// caller on another goroutine.
func (o *OllamaBrain) numCtxFor(highWater int) int {
	if o.audioResolver != nil {
		if o.cfg == nil || o.cfg.OllamaNumCtx <= 0 {
			return nativeContextCeiling
		}
		return min(o.cfg.OllamaNumCtx, nativeContextCeiling)
	}
	if o.cfg == nil {
		return 0
	}
	return max(o.cfg.OllamaNumCtx, highWater)
}

// setContextHighWater publishes the mark to a Warmup that may be reading it.
func (o *OllamaBrain) setContextHighWater(n int) {
	o.stateMu.Lock()
	o.contextHighWater = n
	o.stateMu.Unlock()
}

func (o *OllamaBrain) expandContext() bool {
	if o.cfg == nil || o.cfg.OllamaNumCtx <= 0 {
		return false
	}
	from := o.effectiveNumCtx()
	to := continuity.NextContext(from, o.cfg.ContinuityNumCtxCeil)
	if to == from {
		return false
	}
	o.setContextHighWater(to)
	o.emitContinuity(events.Info{Message: fmt.Sprintf("continuity.num_ctx_expand from=%d to=%d", from, to)})
	return true
}

// NeedsContinuityCompact includes the incoming user text. Skill/tool additions
// are rechecked by the provider before every HTTP request.
func (o *OllamaBrain) NeedsContinuityCompact(input string) bool {
	if o.cfg == nil || o.cfg.ContinuitySoftLimitRatio <= 0 || o.effectiveNumCtx() <= 0 || len(o.history) < 2 {
		return false
	}
	ratio := min(o.cfg.ContinuitySoftLimitRatio, 0.9)
	return float64(o.estimateTokens("")+(len(input)+19)/4) > float64(o.effectiveNumCtx())*ratio
}
func (o *OllamaBrain) ContinuityCompactFiller() bool {
	return o.cfg != nil && o.cfg.ContinuityCompactFiller
}

func (o *OllamaBrain) contextBudgetError(skillCtx string, tools api.Tools) error {
	return o.messagesBudgetError(o.buildMessages(skillCtx), tools)
}

func (o *OllamaBrain) messagesBudgetError(messages []api.Message, tools api.Tools) error {
	if o.audioProjectionBlocked.Load() {
		return ErrAudioProjection
	}
	if o.audioResolver != nil {
		return o.nativeMessagesBudgetError(messages, tools)
	}
	if o.effectiveNumCtx() <= 0 {
		return nil
	}
	bytes := 0
	for _, m := range messages {
		bytes += len(m.Content) + 16
		if len(m.ToolCalls) > 0 {
			encoded, _ := json.Marshal(m.ToolCalls)
			bytes += len(encoded)
		}
	}
	if len(tools) > 0 {
		encoded, _ := json.Marshal(tools)
		bytes += len(encoded)
	}
	est := (bytes + 3) / 4
	if est > o.effectiveNumCtx()*9/10 {
		return fmt.Errorf("%w (%d estimated tokens, num_ctx=%d); shorten the input or compact the conversation", ErrPromptBudget, est, o.effectiveNumCtx())
	}
	return nil
}

// compactionMessages promotes bounded archive summaries as paired tool data.
// Archived tool content must never gain a user/system instruction role. Omit
// recall when it would crowd out the existing conversation and compact prompt.
func (o *OllamaBrain) compactionMessages(skillCtx string) []api.Message {
	messages := o.buildMessages(skillCtx)
	// Explicit-reference recall may already have promoted archive data.
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID == "continuity-recall" {
			return messages
		}
	}
	notes := o.ContinuityNotes()
	if notes == "" || len(messages) < 2 {
		return messages
	}
	recalled := []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "continuity-recall", Function: api.ToolCallFunction{Name: "recall_context", Arguments: api.NewToolCallFunctionArguments()}}}},
		{Role: "tool", ToolName: "recall_context", ToolCallID: "continuity-recall", Content: notes},
	}
	// Insert immediately before the summarize instruction (the last user).
	end := len(messages) - 1
	result := append([]api.Message(nil), messages[:end]...)
	result = append(result, recalled...)
	result = append(result, messages[end])
	if o.messagesBudgetError(result, nil) != nil {
		return messages
	}
	return result
}

// compactForContinuation uses a separate, tools-disabled request. It commits
// history only after a complete, nonempty summary; failure leaves it untouched.
func (o *OllamaBrain) compactForContinuation(ctx context.Context) error {
	if len(o.history) < 2 {
		return nil
	}
	before := append([]api.Message(nil), o.history...)
	messages := o.compactionMessages("")
	messages = append(messages, api.Message{Role: "user", Content: "Summarize the conversation for continuation. Preserve the user's latest request, names, identifiers, decisions, and tool findings. Use at most 300 words. Do not execute tools."})
	if err := o.messagesBudgetError(messages, nil); err != nil {
		return err
	}
	req := o.newChatRequest(messages, nil)
	stream := false
	req.Stream = &stream
	var summary, reason string
	err := o.chat(ctx, req, func(r api.ChatResponse) error { summary += r.Message.Content; reason = r.DoneReason; return nil })
	if err != nil {
		return err
	}
	summary = cleanForVoice(summary)
	if strings.TrimSpace(summary) == "" || reason == "length" {
		return errors.New("continuation summary incomplete")
	}
	// Retain the current user/tool group so no result is orphaned.
	start := lastUserIndex(before)
	if start < 0 {
		start = len(before)
	}
	o.archiveMessages(before[:start])
	o.history = append([]api.Message{{Role: ollamaSharedRole, Content: "Shared conversation notes: " + continuity.Clip(summary, 4096)}}, before[start:]...)
	workingStart := start
	if workingStart > len(o.working) {
		workingStart = len(o.working)
	}
	working := append([]Turn{{Role: "assistant", Content: "Shared conversation notes: " + continuity.Clip(summary, 4096)}}, o.working[workingStart:]...)
	o.working = working
	return nil
}

// recallNotes is an explicit, deterministic lookup: references such as workitem
// IDs, or an explicit request for earlier context, can promote archived notes.
// Ordinary turns keep their byte-stable prompt prefix and incur no recall.
func (o *OllamaBrain) recallNotes() string {
	index := lastUserIndex(o.history)
	if index < 0 {
		return ""
	}
	_, input := decodeOllamaUser(o.history[index].Content)
	input = strings.ToLower(input)
	earlier := strings.Contains(input, "earlier") || strings.Contains(input, "previous") || strings.Contains(input, "what did we")
	var refs []string
	for _, word := range strings.Fields(input) {
		word = strings.Trim(word, ".,?!;:\"'()[]")
		if strings.HasPrefix(word, "wi-") || strings.HasPrefix(word, "job-") {
			refs = append(refs, word)
		}
	}
	if !earlier && len(refs) == 0 {
		return ""
	}
	entries := o.archive.Snapshot()
	var b strings.Builder
	for i := len(entries) - 1; i >= 0; i-- {
		matches := earlier
		for _, ref := range refs {
			matches = matches || strings.Contains(strings.ToLower(entries[i].Summary), ref)
		}
		if !matches {
			continue
		}
		remaining := continuity.NoteBytes - b.Len()
		if remaining <= 3 {
			break
		}
		b.WriteString(continuity.Clip(entries[i].Summary+"\n", remaining))
	}
	return b.String()
}

func recallMessages(notes string) []api.Message {
	return []api.Message{
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "continuity-recall", Function: api.ToolCallFunction{Name: "recall_context", Arguments: api.NewToolCallFunctionArguments()}}}},
		{Role: "tool", ToolName: "recall_context", ToolCallID: "continuity-recall", Content: notes},
	}
}

// ReseedContinuity replaces hot history after same-session compaction. Unlike
// LoadHistory (session restore), this intentionally keeps the session archive.
func (o *OllamaBrain) ReseedContinuity(turns []Turn) {
	o.archiveMessages(o.history)
	// The synthetic summary is shared context, not a reply authored by the
	// currently active persona. Keep the durable canonical transcript intact.
	o.loadHistoryProjectionForPersona(turns, false, true)
	o.maybeEmitContinuityBudget("")
}
