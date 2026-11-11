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
// the harness request context bound, which leaves room for the request,
// results and instructions in the single prompt argument the CLI receives.
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
// not seen do not fit one request. The request then carries a bounded replay.
func (b *Brain) prepareCatchUp(c *SessionCatchUp) {
	if c == nil || b.sessionID == "" {
		return
	}
	if c.Seen >= 0 && c.Seen <= len(c.Turns) {
		if _, fits := catchUpRecords(c.Turns[c.Seen:], b.speakerNames, b.catchUpBudget()); fits {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "obey-voice: claude catch-up does not fit one request; starting a fresh session from a bounded replay\n")
	b.dropSession()
}

// catchUpPrompt renders what this request carries as conversation record: the
// unseen turns into a live session, a compaction summary and the turns after
// it, or a bounded replay into a fresh session, marked when older turns are
// left out.
func (b *Brain) catchUpPrompt(c *SessionCatchUp) string {
	turns := c.Turns
	if seen := c.Seen >= 0 && c.Seen <= len(turns); seen && b.sessionID != "" {
		turns = turns[c.Seen:]
	} else if seen && b.seed != nil {
		turns = append([]Turn{*b.seed}, turns[c.Seen:]...)
	}
	records, _ := catchUpRecords(turns, b.speakerNames, b.catchUpBudget())
	omitted := len(turns) - len(records)
	if len(records) == 0 && omitted == 0 {
		return ""
	}
	parts := []string{systemPrompt(b.cfg, "daemon-history", nil)}
	if omitted > 0 {
		parts = append(parts, fmt.Sprintf("[%d earlier conversation records omitted]", omitted))
	}
	return strings.Join(append(parts, records...), "\n")
}

// catchUpRecords returns the records of the longest tail of turns that fits
// budget bytes, and whether every turn fit.
func catchUpRecords(turns []Turn, names SpeakerNames, budget int) ([]string, bool) {
	records := make([]string, 0, len(turns))
	size, fits := 0, true
	for i := len(turns) - 1; i >= 0; i-- {
		record := cliHistoryRecord(turns[i], names)
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
