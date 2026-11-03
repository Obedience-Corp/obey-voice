package brain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHarnessRoutingLimitPreservesLongConversationAndFollowingTurns(t *testing.T) {
	for _, where := range []string{"latest request", "history"} {
		t.Run(where, func(t *testing.T) {
			b, f := codexFixture(t)
			b.cfg.PromptsDir, b.cfg.MaxHistory = t.TempDir(), 20
			f.response = "I can discuss the supplied document."
			document := "Original document starts here.\n" + strings.Repeat("Document paragraph.\n", 8192) + "\nOriginal document ends here."
			input := "Discuss this document:\n" + document
			if where == "history" {
				b.LoadHistory([]Turn{{Role: "user", Content: "Use Denver, Colorado for all location constraints."}, {Role: "user", Content: document}})
				input = "Summarize the document above."
			}
			before := b.ContinuitySnapshot()
			_, err := b.ClassifyLiveRoute(t.Context(), LiveRouteRequest{Text: input, Conversation: b.ContextHistory()})
			if !errors.Is(err, ErrHarnessRouteContextLimit) {
				t.Fatalf("expected recoverable routing limit, got %v", err)
			}
			if len(f.requests) != 0 || !reflect.DeepEqual(before, b.ContinuitySnapshot()) {
				t.Fatal("routing limit ran inference or changed conversation")
			}
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, HarnessRouting: &HarnessRouting{WorkersAvailable: true, ContextLimited: true}}
			stream, err := b.ThinkStream(t.Context(), input, opts)
			if err != nil {
				t.Fatal(err)
			}
			text, result := collectStream(t, stream)
			if result.Err != nil || text != f.response {
				t.Fatalf("conversation unavailable: %q %+v", text, result)
			}
			r := f.requests[0]
			if r.Tools || !strings.Contains(r.Prompt, input) || !strings.Contains(r.Prompt, "Original document ends here.") {
				t.Fatal("conversation lost original input/context or enabled tools")
			}
			if strings.Contains(r.Prompt, "The host has classified this turn as conversation") || !strings.Contains(r.Prompt, "No worker was dispatched") {
				t.Fatal("fallback misrepresented routing as a chat decision")
			}
			if where == "history" && !strings.Contains(r.Prompt, "Use Denver, Colorado") {
				t.Fatal("fallback lost earlier user constraints")
			}
			// The oversized saved history must not wedge the next short turn. The
			// talking session resumes normally without a second private model call.
			const followup = "What about the second paragraph?"
			_, err = b.ClassifyLiveRoute(t.Context(), LiveRouteRequest{Text: followup, Conversation: b.ContextHistory()})
			if !errors.Is(err, ErrHarnessRouteContextLimit) || len(f.requests) != 1 {
				t.Fatalf("follow-up classification=%v calls=%d", err, len(f.requests))
			}
			stream, err = b.ThinkStream(t.Context(), followup, opts)
			if err != nil {
				t.Fatal(err)
			}
			_, result = collectStream(t, stream)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if len(f.requests) != 2 || f.requests[1].ThreadID != "thread-fixture" || !strings.Contains(f.requests[1].Prompt, followup) {
				t.Fatal("following conversation did not reach the resumed harness")
			}
			if got := b.History(); len(got) < 4 || got[len(got)-2].Content != followup {
				t.Fatal("following user turn was not saved")
			}
		})
	}
}
