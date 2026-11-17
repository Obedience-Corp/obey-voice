package personaruntime

import (
	"strings"
	"testing"
)

// A spawn acknowledgement the host writes after a full window must not push
// the window past the message count that starts a compaction.
func TestSpawnAcknowledgementDoesNotStartACompaction(t *testing.T) {
	c := newLocalConversation(t, 10, "ember")
	c.p.CompactPrompt = "Summarize the conversation."
	c.p.AutoCompactAfter = c.base.MaxHistory * 2
	c.say("One.", "Two.", "Three.", "Four.", "Five.", "look up the weather in Tucson", "What do you remember?")
	if errs := c.errorEvents(); len(errs) > 0 {
		t.Fatalf("errors: %+v", errs)
	}
	saw := false
	for i, body := range c.requests() {
		saw = saw || strings.Contains(string(body), "weather in Tucson")
		if strings.Contains(string(body), "Summarize the conversation.") {
			t.Fatalf("request %d was a compaction summary", i+1)
		}
	}
	if !saw {
		t.Fatal("the spawned line never reached a later request")
	}
}
