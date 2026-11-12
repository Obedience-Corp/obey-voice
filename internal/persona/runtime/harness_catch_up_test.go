package personaruntime

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// sessionProviders are the hosted backends other than Claude whose persona
// keeps its own session while another persona speaks.
var sessionProviders = []string{"grok", "cursor", "codex"}

// harnessRequest is one request a backend keeping its own session received:
// its prompt, the session it resumed, if any, and the session it answered in.
type harnessRequest struct{ Prompt, Resume, Session string }

func harnessRequests(t *testing.T, provider, log string) []harnessRequest {
	t.Helper()
	var out []harnessRequest
	switch provider {
	case "claude":
		if _, err := os.Stat(log); err != nil {
			return nil
		}
		for _, r := range readClaudeRequests(t, log) {
			out = append(out, harnessRequest{r.Prompt, r.Resume, r.Session})
		}
	case "codex":
		for _, turn := range readCodexTurns(t, log) {
			resume := ""
			if turn.Resumed {
				resume = turn.Thread
			}
			out = append(out, harnessRequest{turn.Prompt, resume, turn.Thread})
		}
	default:
		for _, r := range readCLIRequests(t, log) {
			if r.Provider == provider {
				out = append(out, harnessRequest{r.Prompt, r.Resume, r.Session})
			}
		}
	}
	return out
}

// newSessionConversation is a conversation started on Ember, on Ollama, with
// Mira on provider. Every fake backend answers in a session of its own.
func newSessionConversation(t *testing.T, provider string) *claudeConversation {
	t.Helper()
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	t.Setenv("OBEY_CLI_FIXTURE_SESSIONS", "distinct")
	t.Setenv("OBEY_CODEX_FIXTURE_THREADS", "distinct")
	return newConversation(t, 20, "ember", provider, "")
}

func (c *claudeConversation) sessionRequests() []harnessRequest {
	c.t.Helper()
	return harnessRequests(c.t, c.provider, c.log)
}

func (c *claudeConversation) lastSessionRequest() harnessRequest {
	c.t.Helper()
	requests := c.sessionRequests()
	if len(requests) == 0 {
		c.t.Fatalf("no %s requests", c.provider)
	}
	return requests[len(requests)-1]
}

// requireSpeakers checks who gave each reply in turns, "" for a user line.
func requireSpeakers(t *testing.T, turns []brain.Turn, want ...string) {
	t.Helper()
	got := make([]string, len(turns))
	for i, turn := range turns {
		if turn.Role == "assistant" {
			got[i] = turn.PersonaID
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("speakers = %q, want %q", got, want)
	}
}

func TestReturningSessionPersonaResumesItsSessionWithOnlyWhatItMissed(t *testing.T) {
	for _, provider := range sessionProviders {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.say("Note that the door is teal.", "switch to Mira", "What do you think?")
			mira := c.p.Brain
			c.say("switch to Ember", "What color is the door?", "And the car?", "switch to Mira")
			if c.p.Brain != mira {
				t.Fatal("returning to Mira did not reuse its parked provider")
			}
			c.say("Do you agree?")

			transcript := c.selector.Transcript()
			requireSpeakers(t, transcript, "", "ember", "", "mira", "", "mira", "", "ember", "", "ember", "", "ember", "", "mira", "", "mira")
			requests := c.sessionRequests()
			if len(requests) != 2 {
				t.Fatalf("%s requests = %+v", provider, requests)
			}
			first, second := requests[0], requests[1]
			if first.Resume != "" || first.Session == "" {
				t.Fatalf("first request = %+v", first)
			}
			if second.Resume != first.Session {
				t.Fatalf("Mira's session %q did not survive Ember speaking: %+v", first.Session, second)
			}
			if got, want := promptRecords(t, first.Prompt), recordsOf(transcript[2:4]); !slices.Equal(got, want) {
				t.Fatalf("first request records = %+v, want %+v", got, want)
			}
			// Only what Mira missed, each line under its speaker's name.
			if got, want := promptRecords(t, second.Prompt), recordsOf(transcript[6:14]); !slices.Equal(got, want) {
				t.Fatalf("catch-up = %+v, want %+v", got, want)
			}
			requireContains(t, first.Prompt, "User: What do you think?")
			requireContains(t, second.Prompt, `"persona":"Ember"`, "User: Do you agree?")
			for _, req := range requests {
				requireOmits(t, req.Prompt, "You are Ember.", "Note that the door is teal.")
			}
			cursor := c.selector.ContextSnapshots()["mira"]
			if cursor.Session == nil || cursor.Session.From != 2 || cursor.Session.Provider != provider || cursor.Through != len(transcript) || !cursor.Matches(transcript) {
				t.Fatalf("Mira's cursor = %+v for %d turns", cursor, len(transcript))
			}
		})
	}
}

// Muse Spark and Obey hold no session to come back to: each request replays
// the persona's whole share, which includes what it missed.
func TestReturningReplayPersonaReceivesWhatItMissedInItsReplay(t *testing.T) {
	for _, provider := range []string{"muse-spark", "obey"} {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.say("Note that the door is teal.", "switch to Mira", "What do you think?")
			c.say("switch to Ember", "What color is the door?", "And the car?", "switch to Mira", "Do you agree?")

			transcript := c.selector.Transcript()
			prompts := c.hostedPrompts()
			if len(prompts) != 2 {
				t.Fatalf("%s requests = %d", provider, len(prompts))
			}
			if got, want := promptRecords(t, prompts[1]), recordsOf(transcript[2:14]); !slices.Equal(got, want) {
				t.Fatalf("replay = %+v, want %+v", got, want)
			}
			requireOmits(t, prompts[1], "Note that the door is teal.")
		})
	}
}

func TestSessionCatchUpTooLargeForOneRequestStartsAFreshSession(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.base.OllamaNumCtx = 1 << 17
			c.start("ember", nil, nil)
			c.say("switch to Mira", "What do you think?", "switch to Ember")
			// Four lines of about 20 KB each: more than one request carries,
			// though none needs shortening.
			for i := range 4 {
				c.say(strings.Repeat("teal ", 4000) + "LINE-" + string(rune('A'+i)))
			}
			c.say("switch to Mira", "Do you agree?")

			requests := c.sessionRequests()
			if len(requests) != 2 {
				t.Fatalf("%s requests = %d", provider, len(requests))
			}
			first, second := requests[0], requests[1]
			if second.Resume != "" || second.Session == first.Session {
				t.Fatalf("an oversized catch-up resumed the old session: %+v", second)
			}
			transcript := c.selector.Transcript()
			unseen := len(transcript) - 2
			records := promptRecords(t, second.Prompt)
			if len(records) == 0 || len(records) >= unseen {
				t.Fatalf("replay has %d records of %d", len(records), unseen)
			}
			if want := recordsOf(transcript[unseen-len(records) : unseen]); !slices.Equal(records, want) {
				t.Fatalf("replay = %+v, want the newest lines %+v", records, want)
			}
			requireContains(t, second.Prompt, "earlier conversation records omitted]", "LINE-D", "User: Do you agree?")
			requireOmits(t, second.Prompt, "more characters of this line omitted]")
			if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
				t.Fatalf("cursor after the replay = %+v for %d turns", cursor, len(transcript))
			}
		})
	}
}

func TestSessionCatchUpShortensAnOversizedLine(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.base.OllamaNumCtx = 1 << 17
			c.start("ember", nil, nil)
			c.say("switch to Mira", "What do you think?", "switch to Ember")
			c.say("PASTE-START " + strings.Repeat("teal ", 8000) + " PASTE-END")
			c.say("switch to Mira", "Do you agree?")

			requests := c.sessionRequests()
			last := requests[len(requests)-1]
			if len(requests) != 2 || last.Resume != requests[0].Session {
				t.Fatalf("a shortened line did not fit the live session: %+v", requests)
			}
			requireContains(t, last.Prompt, "PASTE-START", "more characters of this line omitted]", "Ember here.", "User: Do you agree?")
			requireOmits(t, last.Prompt, "PASTE-END", "earlier conversation records omitted]")
			if transcript := c.selector.Transcript(); !strings.HasSuffix(transcript[len(transcript)-6].Content, "PASTE-END") {
				t.Fatalf("the record was shortened in the transcript: %q", transcript[len(transcript)-6].Content)
			}
		})
	}
}

// Codex replays its view within max_history, as it always has, and is sent
// lines of any length whole: a missed line trimmed out of that window means a
// new thread.
func TestCodexThreadMissingLinesPastMaxHistoryStartsANewThread(t *testing.T) {
	t.Setenv("OBEY_CODEX_FIXTURE_THREADS", "distinct")
	c := newConversation(t, 3, "ember", "codex", "")
	c.say("switch to Mira", "What do you think?", "switch to Ember", "LINE-ONE", "LINE-TWO", "LINE-THREE", "LINE-FOUR", "switch to Mira", "Do you agree?")

	requests := c.sessionRequests()
	if len(requests) != 2 {
		t.Fatalf("codex requests = %+v", requests)
	}
	if second := requests[1]; second.Resume != "" || second.Session == requests[0].Session {
		t.Fatalf("a thread missing trimmed lines was resumed: %+v", second)
	}
	transcript := c.selector.Transcript()
	if got, want := promptRecords(t, requests[1].Prompt), recordsOf(transcript[10:16]); !slices.Equal(got, want) {
		t.Fatalf("replay = %+v, want the max_history window %+v", got, want)
	}
	requireOmits(t, requests[1].Prompt, "LINE-ONE", "LINE-TWO")
}

func TestCodexThreadIsSentALongLineWhole(t *testing.T) {
	c := newSessionConversation(t, "codex")
	c.say("switch to Mira", "What do you think?", "switch to Ember")
	c.say("PASTE-START " + strings.Repeat("teal ", 8000) + " PASTE-END")
	c.say("switch to Mira", "Do you agree?")

	requests := c.sessionRequests()
	if len(requests) != 2 || requests[1].Resume != requests[0].Session {
		t.Fatalf("Mira's thread was not resumed: %+v", requests)
	}
	requireContains(t, requests[1].Prompt, "PASTE-START", "PASTE-END", "User: Do you agree?")
	requireOmits(t, requests[1].Prompt, "omitted]")
}

func TestOllamaHandsTheFloorToAGrokOrCursorPersonaByName(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.p.RouteHandoff = c.selector.RouteHandoff
			c.setEmberReply("@Mira what color do you think the door is?")
			c.say("Note that the door is teal.")

			transcript := c.selector.Transcript()
			requireSpeakers(t, transcript, "", "ember", "mira")
			request := c.lastSessionRequest()
			if got, want := promptRecords(t, request.Prompt), recordsOf(transcript[1:2]); !slices.Equal(got, want) {
				t.Fatalf("handoff catch-up = %+v, want %+v", got, want)
			}
			requireContains(t, request.Prompt, "The latest agent is handing you the floor")
			requireOmits(t, request.Prompt, "door is teal", "\nUser:")
			// A handoff is not a line the user said.
			for _, turn := range c.p.Brain.History() {
				if turn.Role == "user" {
					t.Fatalf("the handoff was recorded as a user line: %+v", c.p.Brain.History())
				}
			}
			if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Session.From != 1 || cursor.Through != 3 {
				t.Fatalf("Mira's cursor = %+v", cursor)
			}
		})
	}
}

func TestGrokOrCursorTurnAfterCompactionStartsFromTheSummary(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("OBEY_CLI_FIXTURE_SESSIONS", "distinct")
			c := newCLIConversation(t, provider)
			c.provider = provider
			var summary string
			events.Subscribe(c.p.Events, func(e events.ConversationCompacted) { summary = e.Summary })
			c.say("Note that the vault code is cobalt.", "The door is teal.", "Mira")
			c.p.CompactPrompt = "Summarize the conversation."
			if err := c.p.CompactConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			requests := c.sessionRequests()
			summarize := requests[len(requests)-1]
			if summarize.Resume == "" || summary == "" {
				t.Fatalf("summary request = %+v, summary %q", summarize, summary)
			}
			transcript := c.selector.Transcript()
			if got, want := promptRecords(t, summarize.Prompt), recordsOf(transcript[4:6]); !slices.Equal(got, want) {
				t.Fatalf("the summary did not cover the unseen lines: %+v, want %+v", got, want)
			}

			c.say("Mira", "What do you remember?")
			last := c.lastSessionRequest()
			if last.Resume != "" {
				t.Fatalf("the turn after compaction resumed %q", last.Resume)
			}
			transcript = c.selector.Transcript()
			want := append([]promptRecord{{Role: "assistant", Content: summary}}, recordsOf(transcript[6:8])...)
			if got := promptRecords(t, last.Prompt); !slices.Equal(got, want) {
				t.Fatalf("after compaction = %+v, want the summary and the lines after it %+v", got, want)
			}
			if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Through != len(transcript) {
				t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
			}
		})
	}
}

func TestFifthParkedSessionAcrossProvidersRetiresTheLeastRecentlyUsed(t *testing.T) {
	c := newSessionConversation(t, "claude")
	cli, codex := installCLIFixture(t), installCodexSelectorFixture(t)
	logs := map[string]string{"claude": c.log, "grok": cli, "cursor": cli, "codex": codex}
	backends := []struct{ name, provider string }{{"Ava", "claude"}, {"Bea", "grok"}, {"Cal", "cursor"}, {"Dee", "codex"}, {"Eli", "grok"}, {"Fay", "cursor"}}
	providerOf := map[string]string{}
	for _, b := range backends {
		id := strings.ToLower(b.name)
		p := testProfile(id, b.name, id)
		p.Brain.Provider, p.Brain.Model = b.provider, ""
		writeTestProfile(t, c.catalog.Dir, p)
		writeTestPrompt(t, c.base.PromptsDir, id, "You are {agent_name}.")
		providerOf[b.name] = b.provider
	}
	sessionOf := map[string]string{}
	ask := func(name string) harnessRequest {
		t.Helper()
		c.say("switch to "+name, "What do you think?")
		requests := harnessRequests(t, providerOf[name], logs[providerOf[name]])
		last := requests[len(requests)-1]
		if _, ok := sessionOf[name]; !ok {
			sessionOf[name] = last.Session
		}
		return last
	}
	for _, b := range backends {
		ask(b.name)
	}
	parked := func() []string {
		var ids []string
		for _, p := range c.selector.parked {
			ids = append(ids, p.personaID)
		}
		return ids
	}
	if got := parked(); !slices.Equal(got, []string{"bea", "cal", "dee", "eli"}) {
		t.Fatalf("parked = %q", got)
	}
	if again := ask("Bea"); again.Resume != sessionOf["Bea"] {
		t.Fatalf("Bea's parked Grok session was not resumed: %+v", again)
	}
	if again := ask("Ava"); again.Resume != "" {
		t.Fatalf("the least recently used Claude session was not retired: %+v", again)
	}
	if got := parked(); !slices.Equal(got, []string{"dee", "eli", "fay", "bea"}) {
		t.Fatalf("parked = %q", got)
	}
	if again := ask("Dee"); again.Resume != sessionOf["Dee"] || again.Resume == "" {
		t.Fatalf("Dee's parked Codex thread was not resumed: %+v", again)
	}
	if again := ask("Cal"); again.Resume != "" {
		t.Fatalf("the retired Cursor session was resumed: %+v", again)
	}
}

func TestClearRetiresParkedSessionsOfEveryBackend(t *testing.T) {
	for _, provider := range sessionProviders {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.say("switch to Mira", "What do you think?", "switch to Ember")
			if len(c.selector.parked) != 1 || c.selector.parked[0].personaID != "mira" {
				t.Fatalf("parked = %+v", c.selector.parked)
			}
			if err := c.p.ClearConversation(); err != nil {
				t.Fatal(err)
			}
			if len(c.selector.parked) != 0 {
				t.Fatalf("a cleared conversation kept a parked session: %+v", c.selector.parked)
			}
			c.say("switch to Mira", "Hello again.")
			if last := c.lastSessionRequest(); last.Resume != "" {
				t.Fatalf("a cleared conversation resumed Mira's session: %+v", last)
			}
		})
	}
}

func TestSavedSessionCursorSurvivesResume(t *testing.T) {
	for _, provider := range sessionProviders {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.say("Note that the safe code is 4711.", "switch to Mira", "What do you think?", "switch to Ember", "What color is the door?")
			saved := c.saved()
			mira := saved.PersonaContexts["mira"]
			if mira.Session == nil || mira.Session.From != 2 || mira.Session.Provider != provider || mira.Through != 6 || !mira.Matches(saved.Turns) {
				t.Fatalf("saved Mira cursor = %+v", mira)
			}

			c.start(saved.SelectedPersonaID, saved.Turns, saved.PersonaContexts)
			if got := c.selector.ContextSnapshots()["mira"]; got.Session == nil || got.Session.From != 2 || got.Through != mira.Through || got.PrefixHash != mira.PrefixHash {
				t.Fatalf("resumed Mira cursor = %+v, saved %+v", got, mira)
			}
			c.say("switch to Mira", "Do you agree?")
			last := c.lastSessionRequest()
			if last.Resume != "" {
				t.Fatalf("a restarted provider resumed %q", last.Resume)
			}
			transcript := c.selector.Transcript()
			if got, want := promptRecords(t, last.Prompt), recordsOf(transcript[2:12]); !slices.Equal(got, want) {
				t.Fatalf("replay after resume = %+v, want %+v", got, want)
			}
			requireOmits(t, last.Prompt, "4711")
		})
	}
}

func TestLiveSessionThatLostItsCursorStillCatchesUpAfterASwitch(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.say("switch to Mira", "Note that the vault code is cobalt.")
			session := c.lastSessionRequest().Session
			c.selector.transcript.mu.Lock()
			delete(c.selector.transcript.sessions, "mira")
			c.selector.transcript.mu.Unlock()

			c.say("The door is teal.", "switch to Ember", "What color is the car?", "switch to Mira", "And you?")
			last := c.lastSessionRequest()
			if last.Resume != session {
				t.Fatalf("Mira's session was not kept once her cursor was lost: resumed %q, want %q", last.Resume, session)
			}
			requireContains(t, last.Prompt, "What color is the car?", emberReply, "User: And you?")
			requireOmits(t, last.Prompt, "The door is teal.")
			if cursor := c.selector.ContextSnapshots()["mira"]; cursor.Session == nil || cursor.Session.Provider != provider {
				t.Fatalf("restarted cursor = %+v", cursor)
			}
		})
	}
}

// interruptStall stops the turn once provider has started answering a
// stalled request: on its first text, or for Cursor, which sends none before
// its result, once the request reached it.
func (c *claudeConversation) interruptStall(interrupt context.CancelFunc) {
	var once sync.Once
	if c.provider != "cursor" {
		events.Subscribe(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
		return
	}
	go func() {
		for {
			if last := c.sessionRequests(); len(last) > 0 && strings.Contains(last[len(last)-1].Prompt, "Say more. "+claudeStall) {
				once.Do(interrupt)
				return
			}
			select {
			case <-c.t.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
}

func TestStoppedCatchUpDoesNotAdvanceTheCursor(t *testing.T) {
	for _, provider := range sessionProviders {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.say("switch to Mira", "What do you think?", "switch to Ember", "What color is the door?", "switch to Mira")
			before := c.selector.ContextSnapshots()["mira"]

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			turnCtx, interrupt := context.WithCancel(ctx)
			c.interruptStall(interrupt)
			if err := c.p.RunTurnTextMode(turnCtx, "Say more. "+claudeStall); err == nil {
				t.Fatal("interrupted turn reported success")
			}
			interrupt()
			if provider != "codex" {
				if after := c.selector.ContextSnapshots()["mira"]; after.Through != before.Through {
					t.Fatalf("a stopped catch-up advanced the cursor from %d to %d", before.Through, after.Through)
				}
			}
			// The stopped provider is parked and taken up again.
			c.say("switch to Ember", "Hello.", "switch to Mira", "Go on.")
			last := c.lastSessionRequest()
			requireContains(t, last.Prompt, `"content":"What color is the door?"`, `"content":"`+emberReply+`","persona":"Ember"`, `"content":"Hello."`, "User: Go on.")
			if cursor, transcript := c.selector.ContextSnapshots()["mira"], c.selector.Transcript(); cursor.Through != len(transcript) {
				t.Fatalf("cursor = %+v for %d turns", cursor, len(transcript))
			}
		})
	}
}

func TestSessionPersonaNeverReceivesPreInviteLinesOrAnotherPersonasToolTraffic(t *testing.T) {
	for _, provider := range sessionProviders {
		t.Run(provider, func(t *testing.T) {
			c := newSessionConversation(t, provider)
			c.setEmberReply(preInviteEmber)
			c.say(preInviteUser, "switch to Mira", "What do you think?")
			label := providerLabel(provider)
			want := "Mira runs on " + label + ", a hosted service. Only what is said from when it was invited is sent to " + label + ", plus jobs it started earlier and their results; earlier lines stay on this machine."
			if notices := c.hostedNotices(); len(notices) != 1 || notices[0].message != want {
				t.Fatalf("notices = %+v, want %q", notices, want)
			}
			c.setEmberReply(awayEmber)
			c.say("switch to Ember", "Check my notes.")
			ref := c.emberUsesATool()
			c.say("switch to Mira", "What did Ember find?")
			caughtUp := c.lastSessionRequest()
			if caughtUp.Resume == "" {
				t.Fatalf("Mira's parked session was not resumed: %+v", caughtUp)
			}
			requireContains(t, caughtUp.Prompt, "Check my notes.", awayEmber, "Read the vault scan.", "Let me look.", "Your notes say hi.")

			saved := c.saved()
			c.start("mira", saved.Turns, saved.PersonaContexts)
			c.say("Still there?")
			replayed := c.lastSessionRequest()
			if replayed.Resume != "" {
				t.Fatalf("a restarted provider resumed %q", replayed.Resume)
			}
			requireContains(t, replayed.Prompt, "What do you think?", "Let me look.", "Your notes say hi.", "User: Still there?")

			c.p.CompactPrompt = "Summarize the conversation."
			if err := c.p.CompactConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			c.say("And now?")

			requests := c.sessionRequests()
			if len(requests) < 5 {
				t.Fatalf("%s requests = %d", provider, len(requests))
			}
			for _, req := range requests {
				requireOmits(t, req.Prompt, "PRIVATE-4711", "PRE-INVITE", "vault_reader", "call-SECRET-ID", "diary.md", "TOOL OUTPUT SECRET", "vault-scan.png", ref.ID)
			}
			requireOmits(t, c.hostedLog(), "PRIVATE-4711", "PRE-INVITE", "vault_reader", "call-SECRET-ID", "diary.md", "TOOL OUTPUT SECRET", "vault-scan.png", ref.ID)
		})
	}
}
