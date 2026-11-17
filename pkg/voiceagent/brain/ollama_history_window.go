package brain

import (
	"strings"

	"github.com/ollama/ollama/api"
)

// lastUserIndex is the most recent user turn, or -1 if the history has none.
func lastUserIndex(history []api.Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return i
		}
	}
	return -1
}

// With the continuity archive on, hot history is budgeted in user turns: a
// user message plus everything after it up to the next one. A chat turn's
// route call and result, a turn's tool rounds and its reply are kept or
// archived together, so routing bookkeeping costs no conversation.
const (
	archiveHistoryTurnsHigh = 12
	archiveHistoryTurnsLow  = 6
)

// trimHistory bounds retained history with a high/low water mark instead of
// a per-turn sliding window: nothing is dropped until the history passes its
// high mark, then it is cut down to the low mark at a user boundary. Between
// trims the retained prefix is byte-stable, so the server's KV prefix cache
// keeps hitting; a slide-per-turn window shifted the prefix every turn and
// forced a full re-prefill for the rest of the conversation.
func (o *OllamaBrain) trimHistory() {
	defer o.retireAgedImages()
	if start := o.historyTrimStart(); start > 0 {
		if ValidateTextProjection(o.effectiveTurns(o.working[:min(start, len(o.working))])) != nil {
			o.audioProjectionBlocked.Store(true)
			return
		}
		o.archiveTurns(o.history[:start], o.working[:min(start, len(o.working))])
		o.history = append([]api.Message(nil), o.history[start:]...)
		if start <= len(o.working) {
			o.working = deepCopyTurns(o.working[start:])
		}
	}
}

// historyTrimStart is the index the retained window begins at once history
// passes its high mark, or 0 when no trim is due.
//
// Without the archive the marks are MaxHistory*2 and MaxHistory messages.
// With it, the turn marks apply as well, capped by MaxHistory. The message
// marks stay in force so a tool-heavy window never reaches the pipeline's
// count-based compaction trigger (also MaxHistory*2 messages).
func (o *OllamaBrain) historyTrimStart() int {
	high, low := o.cfg.MaxHistory*2, o.cfg.MaxHistory
	if !o.cfg.ContinuityArchiveEnabled {
		if len(o.history) <= high {
			return 0
		}
		return historyWindowStart(o.history, low)
	}
	highTurns := min(o.cfg.MaxHistory, archiveHistoryTurnsHigh)
	lowTurns := min(archiveHistoryTurnsLow, max(1, highTurns/2))
	if len(o.history) <= high && userTurns(o.history) <= highTurns {
		return 0
	}
	return max(historyWindowStart(o.history, low), turnWindowStart(o.history, lowTurns))
}

func userTurns(history []api.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// turnWindowStart is the index of the user message that opens the last turns
// user turns, or 0 when history holds no more than that.
func turnWindowStart(history []api.Message, turns int) int {
	if turns <= 0 {
		return len(history)
	}
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" {
			continue
		}
		seen++
		if seen == turns {
			return i
		}
	}
	return 0
}

// historyWindowStart returns the index where a history window of at most max
// messages begins. A window must not open in the middle of a tool-call group:
// stranded tool results (or a tool-call assistant without its user) make
// Ollama 0.32.14 500 with "no user query found in messages" (ollama#17778).
//
// If the tail would open on a tool result, the window advances to the next
// user. If it would open on an assistant tool-call, it walks back to the
// user that started that exchange — slightly over max, rather than dropping
// the query the in-flight tools belong to.
func historyWindowStart(history []api.Message, max int) int {
	if max <= 0 {
		return len(history)
	}
	start := 0
	if len(history) > max {
		start = len(history) - max
	}
	if start == 0 {
		return start
	}
	if history[start].Role == "assistant" && len(history[start].ToolCalls) > 0 {
		for i := start - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				return i
			}
		}
	}
	if history[start].Role == "user" {
		return start
	}
	for i := start + 1; i < len(history); i++ {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history)
}

// HistoryWindow retains a bounded suffix starting at a user boundary. A
// complete current tool exchange may exceed the limit when its call starts at
// the boundary; a cut through results discards that exchange in full.
func HistoryWindow(turns []Turn, max int) []Turn {
	shape := make([]api.Message, len(turns))
	for i, t := range turns {
		shape[i].Role = t.Role
		if len(t.ToolCalls) > 0 {
			shape[i].ToolCalls = make([]api.ToolCall, len(t.ToolCalls))
		}
	}
	return deepCopyTurns(turns[historyWindowStart(shape, max):])
}

// sanitizeOllamaHistory repairs a session that was saved before ToolCalls
// round-tripped: empty assistant + tool results with no user, which Ollama
// rejects as "no user query found in messages". Compact seeds (a non-empty
// assistant summary) stay.
func sanitizeOllamaHistory(in []api.Message) []api.Message {
	i := 0
	for i < len(in) {
		m := in[i]
		if m.Role == "user" || m.Role == ollamaSharedRole || m.Role == ollamaSharedToolRole {
			break
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" && len(m.ToolCalls) == 0 {
			break
		}
		i++
	}
	in = in[i:]
	return in
}
