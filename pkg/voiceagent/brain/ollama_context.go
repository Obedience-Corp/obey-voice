package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ollama/ollama/api"
)

const toolResultTruncationMarker = "... (truncated to fit context)"

// ensureContextBudget permanently drops the oldest history (user-turn
// aligned) when the estimated prompt would overflow ollama_num_ctx. An
// explicit trim beats the server silently truncating from the top, which
// eats the system prompt — and with it the persona — first. The system
// prompt itself is never trimmed: if it alone exceeds the budget, the
// num_ctx setting is what has to change.
//
// The in-flight user query is pinned. Mid-loop tool results are shrunk
// instead of dropped, because Ollama 0.32.14 + qwen3.8 500s when
// truncation leaves no plain user message (ollama#17778).
func (o *OllamaBrain) ensureContextBudget(skillCtx string) {
	if o.cfg != nil && o.cfg.IsWorker {
		return // Workers summarize completed tool exchanges before the next request.
	}
	if o.audioResolver != nil {
		return
	} // Native refuses pressure instead of silently trimming evidence.
	if o.cfg == nil {
		return
	}
	budget := o.effectiveNumCtx()
	if budget <= 0 {
		return
	}
	// Drop/archive older exchanges before expanding a pressured active turn.
	limit := budget * 9 / 10
	if o.estimateTokens(skillCtx) <= limit {
		return
	}
	trimmed := o.dropOldestBeforeLastUser(skillCtx, limit)
	for o.estimateTokens(skillCtx) > o.effectiveNumCtx()*85/100 && o.expandContext() {
	}
	budget = o.effectiveNumCtx()
	limit = budget * 9 / 10
	if o.estimateTokens(skillCtx) > limit {
		trimmed = o.shrinkToolsToTokenLimit(skillCtx, limit) || trimmed
	}
	if trimmed && !o.budgetWarned {
		o.budgetWarned = true
		fmt.Fprintf(os.Stderr, "obey-voice: prompt near ollama_num_ctx=%d; dropping oldest history or shrinking tool results to stay under budget\n", budget)
	}
}

func (o *OllamaBrain) dropOldestBeforeLastUser(skillCtx string, limit int) bool {
	trimmed := false
	for len(o.history) > 2 && o.estimateTokens(skillCtx) > limit {
		lastUser := lastUserIndex(o.history)
		if lastUser < 0 {
			break
		}
		start := historyWindowStart(o.history, len(o.history)-2)
		if start <= 0 || start >= len(o.history) || start > lastUser {
			break
		}
		if ValidateTextProjection(o.effectiveTurns(o.working[:min(start, len(o.working))])) != nil {
			o.audioProjectionBlocked.Store(true)
			break
		}
		o.archiveMessages(o.history[:start])
		o.history = append([]api.Message(nil), o.history[start:]...)
		if start < len(o.working) {
			o.working = append([]Turn(nil), o.working[start:]...)
		} else {
			o.working = []Turn{}
		}
		trimmed = true
	}
	return trimmed
}

// shrinkToolsToTokenLimit assigns remaining prompt bytes to tool bodies,
// newest first, so the request fits limit tokens. Older results shrink
// first; a tool may become empty if the budget is that tight.
func (o *OllamaBrain) shrinkToolsToTokenLimit(skillCtx string, limit int) bool {
	if o.estimateTokens(skillCtx) <= limit {
		return false
	}
	remaining := limit*4 - (o.promptBytes(skillCtx) - toolContentBytes(o.history))
	changed := shrinkToolResults(o.history, remaining)
	if changed {
		for i, m := range o.history {
			if i >= len(o.working) || !isToolContentRole(m.Role) {
				continue
			}
			content := m.Content
			if m.Role == ollamaSharedToolRole {
				t := o.working[i]
				name := t.PersonaDisplayName
				if t.PersonaID == "" {
					_, name, _ = precedingToolOwner(o.working, i)
				}
				prefix := attributedToolContent(name, t.ToolName, "")
				if strings.HasPrefix(content, prefix) {
					content = strings.TrimPrefix(content, prefix)
				} else {
					content = ""
				}
			}
			o.working[i].Content = content
		}
	}
	return changed
}

func shrinkToolResults(history []api.Message, remainingBytes int) bool {
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	var tools []int
	for i, m := range history {
		if isToolContentRole(m.Role) {
			tools = append(tools, i)
		}
	}
	keep := make([]int, len(history))
	budget := remainingBytes
	for t := len(tools) - 1; t >= 0; t-- {
		i := tools[t]
		n := min(len(history[i].Content), budget)
		keep[i] = n
		budget -= n
	}
	changed := false
	for _, i := range tools {
		cur := history[i].Content
		want := keep[i]
		if want >= len(cur) {
			continue
		}
		history[i].Content = truncateToBytes(cur, want)
		changed = true
	}
	return changed
}

func truncateToBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	marker := toolResultTruncationMarker
	if n <= len(marker) {
		return marker[:n]
	}
	end := n - len(marker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}

func toolContentBytes(history []api.Message) int {
	n := 0
	for _, m := range history {
		if isToolContentRole(m.Role) {
			n += len(m.Content)
		}
	}
	return n
}

func isToolContentRole(role string) bool {
	return role == "tool" || role == ollamaSharedToolRole
}

// estimateTokens coarsely sizes the next request (≈4 bytes per token plus
// per-message overhead). It only needs to be accurate enough to trim before
// the server truncates.
func (o *OllamaBrain) estimateTokens(skillCtx string) int {
	return o.promptBytes(skillCtx) / 4
}

func (o *OllamaBrain) promptBytes(skillCtx string) int {
	n := len(o.fullSystemPrompt) + len(o.conversationIdentityGuidance()) + len(currentRequestContext(o.cfg, StreamOptions{})) + len(currentRequestReminder(o.cfg, StreamOptions{})) + len(skillCtx)
	if recall := o.recallNotes(); recall != "" {
		n += len(recall) + 256
	}
	for _, m := range o.history {
		n += len(m.Content) + 16
		if len(m.ToolCalls) > 0 {
			encoded, _ := json.Marshal(m.ToolCalls)
			n += len(encoded)
		}
	}
	return n
}
