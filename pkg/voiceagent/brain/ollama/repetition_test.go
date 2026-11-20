package ollama

import (
	"strings"
	"testing"
)

func TestReplyPrefixGateWhitespaceAndChunks(t *testing.T) {
	const gardenReply = "Keep the blue flowers by the garden path."
	for _, size := range []int{1, 7, 1024} {
		g := replyPrefixGate{prior: NormalizeReplyPrefix(gardenReply)}
		reply := strings.ReplaceAll(gardenReply, " ", "\n  ") + " New details."
		for offset := 0; offset < len(reply); offset += size {
			if text := g.feed(reply[offset:min(offset+size, len(reply))]); text != "" {
				t.Fatalf("size=%d leaked %q", size, text)
			}
		}
		if !g.repeat || g.release() != reply {
			t.Fatalf("size=%d failed to preserve whole reply", size)
		}
	}
}

func TestRestatementSkip(t *testing.T) {
	const already = "The test run finished."
	tests := []struct {
		soFar string
		skip  bool
	}{
		{"", false},
		{"The test", true},
		{"The test run finished.", true},
		{"The test run finished. And then this.", false},
		{"Something else entirely.", false},
	}
	for _, tt := range tests {
		if got := restatementSkip(already, tt.soFar); got != tt.skip {
			t.Errorf("restatementSkip(%q, %q) = %v, want %v", already, tt.soFar, got, tt.skip)
		}
	}
	if restatementSkip("", "hello") {
		t.Error("empty already must not skip; that is the first iteration")
	}
	if !restatementSkip(already, "<think>plan a greeting</think>"+already) {
		t.Error("tagged restatement of the same reply must skip")
	}
}
