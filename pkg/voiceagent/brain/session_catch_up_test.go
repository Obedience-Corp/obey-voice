package brain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
)

func catchUpTurns() []Turn {
	return []Turn{
		{Role: "user", Content: "Note the vault code is cobalt."},
		{Role: "assistant", Content: "Noted, cobalt.", PersonaID: "rex", PersonaDisplayName: "Rex", ResponseID: "r1"},
		{Role: "user", Content: "Mira, what color is the door?", Speaker: "speaker-2"},
		{Role: "assistant", Content: "The door is teal.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
	}
}

func streamOnce(t *testing.T, b *Brain, input string, opts StreamOptions) StreamResult {
	t.Helper()
	s, err := b.ThinkStream(context.Background(), input, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, res := collectStream(t, s)
	return res
}

func TestCatchUpSendsOnlyUnseenTurnsIntoLiveSession(t *testing.T) {
	fake := &fakeClaudeClient{streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Agreed."), resultMsg("sess-1")}}}}
	b := newTestBrain(fake)
	b.sessionID = "sess-1"
	if err := b.AppendAssistantResponseWithIdentity("Host-authored update.", TurnIdentity{PersonaID: "rex", PersonaDisplayName: "Rex"}); err != nil {
		t.Fatal(err)
	}
	if res := streamOnce(t, b, "Rex, do you agree?", StreamOptions{CatchUp: &SessionCatchUp{Turns: catchUpTurns(), Seen: 2}}); res.Err != nil {
		t.Fatal(res.Err)
	}
	prompt := fake.streamPrompts[0]
	if got := fake.streamOpts[0].ResumeID; got != "sess-1" {
		t.Fatalf("ResumeID = %q", got)
	}
	header := systemPrompt(b.cfg, "daemon-history", nil)
	for _, want := range []string{header, `"speaker":"speaker-2"`, "Mira, what color is the door?", `"persona":"Mira"`, "The door is teal.", "User: Rex, do you agree?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"cobalt", "Recent conversation:", "Previous assistant output from the host", "Host-authored update."} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt carries %q:\n%s", unwanted, prompt)
		}
	}
	if strings.Index(prompt, "The door is teal.") > strings.Index(prompt, "User: Rex, do you agree?") {
		t.Fatalf("the record must precede the request:\n%s", prompt)
	}
}

func TestCatchUpReplaysEverythingSharedIntoFreshSession(t *testing.T) {
	fake := &fakeClaudeClient{streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Agreed."), resultMsg("sess-2")}}}}
	b := newTestBrain(fake)
	b.LoadHistory([]Turn{{Role: "user", Content: "A line the host never shared."}})
	if res := streamOnce(t, b, "Rex, do you agree?", StreamOptions{CatchUp: &SessionCatchUp{Turns: catchUpTurns(), Seen: 2}}); res.Err != nil {
		t.Fatal(res.Err)
	}
	prompt := fake.streamPrompts[0]
	if fake.streamOpts[0].ResumeID != "" {
		t.Fatalf("a fresh session resumed %q", fake.streamOpts[0].ResumeID)
	}
	for _, want := range []string{"cobalt", "Noted, cobalt.", "The door is teal."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("replay lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "never shared") || strings.Contains(prompt, "omitted]") {
		t.Fatalf("replay carries the provider's own history or an omission:\n%s", prompt)
	}
}

func TestCatchUpTooLargeForOneRequestStartsFreshSessionWithBoundedReplay(t *testing.T) {
	fake := &fakeClaudeClient{streamScripts: []streamScript{{msgs: []claude.Message{assistantMsg("Agreed."), resultMsg("sess-2")}}}}
	b := newTestBrain(fake)
	b.sessionID = "sess-1"
	turns := catchUpTurns()
	turns = append(turns[:2], append([]Turn{{Role: "user", Content: strings.Repeat("long ", maxCatchUpBytes/5)}}, turns[2:]...)...)
	if res := streamOnce(t, b, "Rex, do you agree?", StreamOptions{CatchUp: &SessionCatchUp{Turns: turns, Seen: 2}}); res.Err != nil {
		t.Fatal(res.Err)
	}
	prompt := fake.streamPrompts[0]
	if fake.streamOpts[0].ResumeID != "" {
		t.Fatalf("an oversized catch-up resumed %q", fake.streamOpts[0].ResumeID)
	}
	if !strings.Contains(prompt, "[3 earlier conversation records omitted]") || strings.Contains(prompt, "long long") || strings.Contains(prompt, "cobalt") {
		t.Fatalf("replay is not bounded with an omission marker:\n%.2000s", prompt)
	}
	if !strings.Contains(prompt, "Mira, what color is the door?") || !strings.Contains(prompt, "The door is teal.") {
		t.Fatalf("replay lost the newest lines:\n%s", prompt)
	}
	if len(prompt) > maxCatchUpBytes {
		t.Fatalf("prompt = %d bytes", len(prompt))
	}
}

func TestCatchUpRejectedResumeRetriesWithReplay(t *testing.T) {
	fake := &fakeClaudeClient{streamScripts: []streamScript{
		{err: errors.New("session not found")},
		{msgs: []claude.Message{assistantMsg("Agreed."), resultMsg("sess-2")}},
	}}
	b := newTestBrain(fake)
	b.sessionID = "stale"
	if res := streamOnce(t, b, "Rex, do you agree?", StreamOptions{CatchUp: &SessionCatchUp{Turns: catchUpTurns(), Seen: 2}}); res.Err != nil {
		t.Fatal(res.Err)
	}
	if fake.streamCalls != 2 || fake.streamOpts[0].ResumeID != "stale" || fake.streamOpts[1].ResumeID != "" {
		t.Fatalf("calls = %d, resumes = %q, %q", fake.streamCalls, fake.streamOpts[0].ResumeID, fake.streamOpts[1].ResumeID)
	}
	if strings.Contains(fake.streamPrompts[0], "cobalt") || !strings.Contains(fake.streamPrompts[1], "cobalt") {
		t.Fatalf("the retry must replay what the lost session held:\n%s", fake.streamPrompts[1])
	}
}

func TestCatchUpBudgetStaysUnderSessionFuse(t *testing.T) {
	b := newTestBrain(&fakeClaudeClient{})
	if got := b.catchUpBudget(); got != maxCatchUpBytes {
		t.Fatalf("budget = %d", got)
	}
	b.cfg.ClaudeMaxSessionTokens = 1000
	if got := b.catchUpBudget(); got != 2000 {
		t.Fatalf("budget with a 1000-token fuse = %d", got)
	}
}
