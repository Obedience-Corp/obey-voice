package brain

import (
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
)

func TestCLIHostReplyKeepsIdentityInResumeAndRestoredPrompts(t *testing.T) {
	for _, name := range []string{"claude", "cursor", "grok"} {
		for _, restore := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/resume", true: "/restore"}[restore], func(t *testing.T) {
				var provider Provider
				var request func() (string, string)
				switch name {
				case "claude":
					client := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "Acknowledged."}}}
					b := newTestBrain(client)
					b.sessionID = "live-session"
					provider = b
					request = func() (string, string) { return client.runPrompts[0], client.runOpts[0].ResumeID }
				case "cursor":
					client := &fakeCursorClient{}
					b := newTestCursorBrain(client)
					b.sessionID = "live-session"
					provider = b
					request = func() (string, string) { return client.prompts[0], client.opts[0].Resume }
				case "grok":
					client := &fakeGrokClient{}
					b := newTestGrokBrain(client)
					b.sessionID = "live-session"
					provider = b
					request = func() (string, string) { return client.runPrompts[0], client.runOpts[0].ResumeID }
				}
				appender := provider.(IdentityHistoryAppender)
				identity := TurnIdentity{PersonaID: "obey", PersonaDisplayName: "Obey", PromptHash: "private-prompt-hash", ResponseID: "private-response-id"}
				if err := appender.AppendAssistantResponseWithIdentity("The worker found three files.", identity); err != nil {
					t.Fatal(err)
				}
				if err := appender.AppendAssistantResponseWithIdentity("  ", identity); err == nil {
					t.Fatal("empty reply accepted")
				}
				if len(provider.History()) != 1 {
					t.Fatal("empty reply changed history")
				}
				turn := provider.History()[0]
				if turn.PersonaID != identity.PersonaID || turn.PersonaDisplayName != identity.PersonaDisplayName || turn.PromptHash != identity.PromptHash || turn.ResponseID != identity.ResponseID || turn.At.IsZero() {
					t.Fatalf("missing attribution: %+v", turn)
				}
				if restore {
					provider.LoadHistory(append([]Turn(nil), provider.History()...))
				}
				if _, err := provider.ThinkFull(t.Context(), "What did Obey find?", StreamOptions{}); err != nil {
					t.Fatal(err)
				}
				prompt, resume := request()
				if !strings.Contains(prompt, "Obey: The worker found three files.") || strings.Contains(prompt, "Samantha: The worker found") || strings.Contains(prompt, identity.ResponseID) || strings.Contains(prompt, identity.PromptHash) {
					t.Fatalf("incorrect host context: %s", prompt)
				}
				if (!restore && resume != "live-session") || (restore && resume != "") {
					t.Fatalf("restore=%t resume=%q", restore, resume)
				}
			})
		}
	}
}
