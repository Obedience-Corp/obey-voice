package brain

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// SessionCatchUp is the part of the host transcript that a provider keeping
// its own session may receive. The first Seen turns are already in its
// live session.
type SessionCatchUp struct {
	Turns []Turn
	Seen  int
}

// maxCatchUpBytes bounds the conversation record one request carries: half
// the harness request context bound. It does not bound the request or its
// results; a prompt past maxInlinePromptBytes is piped on stdin, so no
// prompt becomes one argument over the 128 KiB Linux allows.
const maxCatchUpBytes = harnessRouteContextBytes / 2

// catchUpBudget also keeps a replay under half the session fuse when one is
// set, so a fresh session never trips the fuse by itself.
func (b *Brain) catchUpBudget() int {
	budget := maxCatchUpBytes
	if b.cfg != nil && b.cfg.ClaudeMaxSessionTokens > 0 {
		// About four bytes per token, as estimateSessionTokens counts.
		budget = min(budget, b.cfg.ClaudeMaxSessionTokens*2)
	}
	return budget
}

// prepareCatchUp starts a fresh session when the turns the live session has
// not seen do not fit one request, each long line already shortened. The
// request then carries a bounded replay. A summary request keeps the live
// session, which holds what the summary must cover.
func (b *Brain) prepareCatchUp(c *SessionCatchUp, summary bool) {
	if c == nil || b.sessionID == "" || summary {
		return
	}
	budget := b.catchUpBudget()
	if c.Seen >= 0 && c.Seen <= len(c.Turns) {
		if _, fits := catchUpRecords(c.Turns[c.Seen:], b.speakerNames, budget, budget/2); fits {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "obey-voice: claude catch-up does not fit one request; starting a fresh session from a bounded replay\n")
	b.dropSession()
}

// catchUpPrompt renders what this request carries as conversation record: the
// unseen turns into a live session, a compaction summary and the turns after
// it, or a bounded replay into a fresh session. Turns left out are counted in
// a marker, and a shortened line says so in its own record.
func (b *Brain) catchUpPrompt(c *SessionCatchUp) string {
	budget := b.catchUpBudget()
	lineBudget := budget / 2
	turns := c.Turns
	var summary []string
	if seen := c.Seen >= 0 && c.Seen <= len(turns); seen && b.sessionID != "" {
		turns = turns[c.Seen:]
	} else if seen && b.seed != nil {
		turns = turns[c.Seen:]
		summary = []string{boundedRecord(*b.seed, b.speakerNames, lineBudget)}
		budget -= len(summary[0]) + 1
	}
	records, _ := catchUpRecords(turns, b.speakerNames, budget, lineBudget)
	omitted := len(turns) - len(records)
	if len(summary) == 0 && len(records) == 0 && omitted == 0 {
		return ""
	}
	parts := append([]string{systemPrompt(b.cfg, "daemon-history", nil)}, summary...)
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("[%d earlier conversation records omitted]", omitted))
	}
	return strings.Join(append(parts, records...), "\n")
}

// catchUpRecords returns the records of the longest tail of turns that fits
// budget bytes, each at most lineBudget, and whether every turn fit.
func catchUpRecords(turns []Turn, names SpeakerNames, budget, lineBudget int) ([]string, bool) {
	records := make([]string, 0, len(turns))
	size, fits := 0, true
	for i := len(turns) - 1; i >= 0; i-- {
		record := boundedRecord(turns[i], names, lineBudget)
		if size+len(record)+1 > budget {
			fits = false
			break
		}
		size += len(record) + 1
		records = append(records, record)
	}
	slices.Reverse(records)
	return records, fits
}

// boundedRecord is the record of t, its line shortened with a marker when the
// record would exceed limit bytes.
func boundedRecord(t Turn, names SpeakerNames, limit int) string {
	record := cliHistoryRecord(t, names)
	if len(record) <= limit {
		return record
	}
	line := []rune(t.Content)
	for keep := len(line) * limit / len(record); keep > 0; keep = keep * 3 / 4 {
		t.Content = string(line[:keep]) + fmt.Sprintf(" [%d more characters of this line omitted]", len(line)-keep)
		if record = cliHistoryRecord(t, names); len(record) <= limit {
			return record
		}
	}
	t.Content = fmt.Sprintf("[a line of %d characters omitted]", len(line))
	return cliHistoryRecord(t, names)
}
