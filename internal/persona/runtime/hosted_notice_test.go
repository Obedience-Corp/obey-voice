package personaruntime

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const miraNotice = "Mira runs on Claude, a hosted service. Only what is said from when it was invited is sent to Claude; earlier lines stay on this machine."

type hostedNoticeEvent struct {
	message        string
	claudeRequests int
}

func (c *claudeConversation) hostedNotices() []hostedNoticeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []hostedNoticeEvent
	for _, n := range c.notices {
		if strings.Contains(n.message, "a hosted service") {
			out = append(out, n)
		}
	}
	return out
}

func (c *claudeConversation) addClaudePersona(id, display string) {
	c.t.Helper()
	profile := testProfile(id, display, id)
	profile.Brain.Provider, profile.Brain.Model = "claude", ""
	writeTestProfile(c.t, c.catalog.Dir, profile)
	writeTestPrompt(c.t, c.base.PromptsDir, id, "You are {agent_name}.")
}

func TestHostedNoticeComesOnceBeforeTheFirstRequestToTheHostedProvider(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.say("Note that the vault code is cobalt.", "switch to Mira")
	if got := c.hostedNotices(); len(got) != 0 {
		t.Fatalf("a notice came before a hosted persona was asked anything: %+v", got)
	}

	c.say("What do you think?")
	got := c.hostedNotices()
	if len(got) != 1 || got[0].message != miraNotice || got[0].claudeRequests != 0 {
		t.Fatalf("notices = %+v, want %q before any request to Claude", got, miraNotice)
	}
	if n := len(readClaudeRequests(t, c.log)); n != 1 {
		t.Fatalf("claude requests = %d", n)
	}

	c.say("And now?", "switch to Ember", "Anything new?", "switch to Mira", "Back again.")
	if got := c.hostedNotices(); len(got) != 1 {
		t.Fatalf("a second notice for the same provider: %+v", got)
	}

	if err := c.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	c.say("Fresh start.")
	got = c.hostedNotices()
	if len(got) != 2 || got[1].message != "Mira runs on Claude, a hosted service. What is said to it is sent to Claude." {
		t.Fatalf("clearing did not reset the notice: %+v", got)
	}
}

func TestHostedNoticeIsSavedWithTheSessionAndNotRepeatedOnResume(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.say("Hello.", "switch to Mira", "What do you think?")
	if saved := c.saved(); !slices.Equal(saved.HostedNotified, []string{"claude"}) {
		t.Fatalf("saved notices = %q", saved.HostedNotified)
	}
	c.sess = c.saved()
	c.start("mira", c.sess.Turns, c.sess.PersonaContexts)
	c.say("Still there?")
	if got := c.hostedNotices(); len(got) != 1 {
		t.Fatalf("resume repeated the notice: %+v", got)
	}
}

func TestClearingDropsTheSavedNotices(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.say("Hello.", "switch to Mira", "What do you think?", "switch to Ember")
	if err := c.p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	c.say("Fresh start.")
	if saved := c.saved(); len(saved.HostedNotified) != 0 {
		t.Fatalf("a cleared conversation kept its notices: %q", saved.HostedNotified)
	}
	c.say("switch to Mira", "Hello again.")
	if saved := c.saved(); !slices.Equal(saved.HostedNotified, []string{"claude"}) || len(c.hostedNotices()) != 2 {
		t.Fatalf("saved = %q, notices = %+v", saved.HostedNotified, c.hostedNotices())
	}
}

func TestHostedNoticeNeverReachesAModelOrTheTranscript(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.say("Hello.", "switch to Mira", "What do you think?", "switch to Ember", "Thoughts?", "switch to Mira", "Anything else?")
	if len(c.hostedNotices()) != 1 {
		t.Fatalf("notices = %+v", c.hostedNotices())
	}
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "hosted service")
	}
	for _, request := range c.ollamaRequests() {
		for _, m := range request {
			requireOmits(t, m.Content, "hosted service")
		}
	}
	for _, turn := range c.selector.Transcript() {
		requireOmits(t, turn.Content, "hosted service")
	}
	for _, r := range c.responses() {
		requireOmits(t, r.Response, "hosted service")
	}
	for _, turn := range c.saved().Turns {
		requireOmits(t, turn.Content, "hosted service")
	}
}

func TestHostedNoticeIsOnlySpokenWhenTheConversationDidNotStartOnThatProvider(t *testing.T) {
	spoken := func(c *claudeConversation, id string) (pipelineNotice string, spoken bool) {
		t.Helper()
		selected, err := c.selector.Select(t.Context(), id, "explicit")
		if err != nil {
			t.Fatal(err)
		}
		notice, ok := c.selector.HostedNotice(responseEnvelope(selected))
		if !ok {
			t.Fatal("no notice for a hosted persona")
		}
		return notice.Text, notice.Spoken
	}
	mixed := newMixedConversation(t, 20, "ember")
	mixed.say("Hello.")
	if text, voiced := spoken(mixed, "mira"); !voiced || text != miraNotice {
		t.Fatalf("conversation started on Ollama: spoken = %v, %q", voiced, text)
	}
	plain := newClaudeConversation(t, 20)
	const plainNotice = "Mira runs on Claude, a hosted service. What is said to it is sent to Claude."
	if text, voiced := spoken(plain, "mira"); voiced || text != plainNotice {
		t.Fatalf("conversation started on Claude: spoken = %v, %q; the line must be shown but not spoken", voiced, text)
	}
}

// The first notice of a conversation that started on Claude is shown only, but
// one telling the user that a persona on Claude now receives the whole
// conversation is spoken too.
func TestHostedNoticeThatWidensTheShareIsSpokenWhereTheConversationStarted(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.addClaudePersona("nova", "Nova")
	turns := []brain.Turn{
		{Role: "user", Content: "The vault code is cobalt."},
		{Role: "assistant", Content: "Noted.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	c.start("mira", turns, nil)
	first, ok := c.selector.LiveHostedNotice()
	if !ok || first.Spoken || first.Text != miraNotice {
		t.Fatalf("first notice = %q, spoken %v; a conversation that started on Claude shows it only", first.Text, first.Spoken)
	}
	c.say("What do you think?")
	if got := c.hostedNotices(); len(got) != 1 || got[0].message != miraNotice {
		t.Fatalf("notices = %+v", got)
	}

	widened := c.base.Clone()
	widened.HostedPersonaSharing = "whole_conversation"
	if err := c.selector.Reload(t.Context(), widened); err != nil {
		t.Fatal(err)
	}
	selected, err := c.selector.Select(t.Context(), "nova", "explicit")
	if err != nil {
		t.Fatal(err)
	}
	wider, ok := c.selector.HostedNotice(responseEnvelope(selected))
	const novaNotice = "Nova runs on Claude, a hosted service. The whole conversation is sent to Claude."
	if !ok || !wider.Spoken || wider.Text != novaNotice {
		t.Fatalf("widening notice = %q (due %v), spoken %v; it must be spoken", wider.Text, ok, wider.Spoken)
	}
}

func TestMarkersIdentifyHostedPersonasOnSelectionsAndReplies(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	if c.selector.Snapshot().Hosted {
		t.Fatal("a persona on Ollama is marked hosted")
	}
	c.say("Hello.", "switch to Mira", "What do you think?")
	if s := c.selector.Snapshot(); !s.Hosted || s.BrainProvider != "claude" {
		t.Fatalf("selection = %+v", s)
	}
	byPersona := map[string]events.ResponseEnvelope{}
	for _, r := range c.responses() {
		if r.Envelope != nil {
			byPersona[r.Envelope.PersonaID] = *r.Envelope
		}
	}
	if m := byPersona["mira"]; !m.Hosted || m.BrainProvider != "claude" {
		t.Fatalf("Mira's reply envelope = %+v", m)
	}
	if e := byPersona["ember"]; e.Hosted {
		t.Fatalf("Ember's reply envelope = %+v", e)
	}
}

func TestWholeConversationSharingStillStripsOtherPersonasToolTraffic(t *testing.T) {
	turns := []brain.Turn{
		{Role: "user", Content: "Ember, check my notes.", Attachments: []chatmedia.Ref{{ID: "a1", Name: "vault-scan.png"}}},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []brain.TurnToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"cat ~/private/diary.md"}`}}, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
		{Role: "tool", ToolName: "bash", ToolCallID: "c1", Content: "TOOL OUTPUT SECRET"},
		{Role: "assistant", Content: "Your notes say hi.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	for _, tc := range []struct {
		name, sharing string
		earlier       bool
	}{
		{"default sends from the invitation", "", false},
		{"from_invite sends from the invitation", "from_invite", false},
		{"whole_conversation sends from the start", "whole_conversation", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMixedConversation(t, 20, "ember")
			c.base.HostedPersonaSharing = tc.sharing
			c.start("ember", turns, nil)
			c.say("switch to Mira", "What did Ember find?")
			prompt := readClaudeRequests(t, c.log)[0].Prompt
			notices := c.hostedNotices()
			if len(notices) != 1 || strings.Contains(notices[0].message, "The whole conversation") != tc.earlier || strings.Contains(notices[0].message, "stay on this machine") == tc.earlier {
				t.Fatalf("the notice does not describe what is sent (earlier lines sent: %v): %+v", tc.earlier, notices)
			}
			requireContains(t, prompt, "What did Ember find?")
			requireOmits(t, prompt, "diary.md", "TOOL OUTPUT SECRET", "tool_call", `"bash"`, "vault-scan.png")
			if tc.earlier {
				requireContains(t, prompt, "Ember, check my notes.", "Your notes say hi.")
			} else {
				requireOmits(t, prompt, "Ember, check my notes.", "Your notes say hi.")
			}
		})
	}
}

func TestSharingSettingAppliesToPersonasFirstAddressedAfterTheChange(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.addClaudePersona("nova", "Nova")
	c.say("The vault code is cobalt.", "switch to Mira", "Hello Mira.")
	from := c.saved().PersonaContexts["mira"].Session.From

	widened := c.base.Clone()
	widened.HostedPersonaSharing = "whole_conversation"
	if err := c.selector.Reload(t.Context(), widened); err != nil {
		t.Fatal(err)
	}
	c.say("Anything more?")
	if cursor := c.saved().PersonaContexts["mira"]; cursor.Session == nil || cursor.Session.From != from {
		t.Fatalf("a session already started under from_invite moved its share start: %+v, was %d", cursor, from)
	}
	for _, req := range readClaudeRequests(t, c.log) {
		requireOmits(t, req.Prompt, "cobalt")
	}

	c.say("switch to Nova", "What was the code?")
	last := lastClaudeRequest(t, c.log)
	requireContains(t, last.Prompt, "The vault code is cobalt.")
	if cursor := c.saved().PersonaContexts["nova"]; cursor.Session == nil || cursor.Session.From != 0 {
		t.Fatalf("Nova's cursor = %+v, want the whole conversation", cursor)
	}
}

func TestHostedNoticeOfAResumedConversationSavedBeforeNoticesSaysWhatIsSent(t *testing.T) {
	c := newClaudeConversation(t, 20)
	turns := []brain.Turn{
		{Role: "user", Content: "Note the door is teal."},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
	}
	c.start("mira", turns, brain.PersonaContexts{"mira": brain.SessionContext(turns, 0, len(turns))})
	c.say("Where were we?")
	notices := c.hostedNotices()
	if len(notices) != 1 || notices[0].message != "Mira runs on Claude, a hosted service. The whole conversation is sent to Claude." {
		t.Fatalf("notices = %+v", notices)
	}
}

func TestHostedNoticeIsGivenAgainWhenAPersonaOnTheSameProviderReceivesMore(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.addClaudePersona("nova", "Nova")
	c.addClaudePersona("iris", "Iris")
	c.say("The vault code is cobalt.", "switch to Mira", "What do you think?")
	if got := c.hostedNotices(); len(got) != 1 || got[0].message != miraNotice {
		t.Fatalf("notices = %+v", got)
	}

	widened := c.base.Clone()
	widened.HostedPersonaSharing = "whole_conversation"
	if err := c.selector.Reload(t.Context(), widened); err != nil {
		t.Fatal(err)
	}
	c.say("Anything more?")
	if got := c.hostedNotices(); len(got) != 1 {
		t.Fatalf("a session that kept its share was announced again: %+v", got)
	}

	before := len(readClaudeRequests(t, c.log))
	c.say("switch to Nova", "What was the code?")
	got := c.hostedNotices()
	const novaNotice = "Nova runs on Claude, a hosted service. The whole conversation is sent to Claude."
	if len(got) != 2 || got[1].message != novaNotice || got[1].claudeRequests != before {
		t.Fatalf("notices = %+v, want %q before Nova's first request (request %d)", got, novaNotice, before)
	}
	requireContains(t, lastClaudeRequest(t, c.log).Prompt, "The vault code is cobalt.")

	c.say("switch to Iris", "What is new?")
	if got := c.hostedNotices(); len(got) != 2 {
		t.Fatalf("a second persona with the same share was announced again: %+v", got)
	}
	if saved := c.saved(); !slices.Equal(saved.HostedNotified, []string{"claude"}) || !slices.Equal(saved.HostedNotifiedWhole, []string{"claude"}) {
		t.Fatalf("saved notices = %q, whole = %q", saved.HostedNotified, saved.HostedNotifiedWhole)
	}
	c.sess = c.saved()
	c.start("iris", c.sess.Turns, c.sess.PersonaContexts)
	c.say("Still there?", "switch to Nova", "And you?")
	if got := c.hostedNotices(); len(got) != 2 {
		t.Fatalf("resume repeated a notice already given for the whole conversation: %+v", got)
	}
}

func TestHostedNoticeSavedWithoutItsShareCountsAsFromTheInvitation(t *testing.T) {
	turns := []brain.Turn{
		{Role: "user", Content: "The vault code is cobalt."},
		{Role: "assistant", Content: "Noted.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
	}
	for _, tc := range []struct {
		sharing string
		notice  string
	}{
		{"from_invite", ""},
		{"whole_conversation", "Mira runs on Claude, a hosted service. The whole conversation is sent to Claude."},
	} {
		t.Run(tc.sharing, func(t *testing.T) {
			c := newMixedConversation(t, 20, "ember")
			c.base.HostedPersonaSharing = tc.sharing
			c.sess.HostedNotified = []string{"claude"}
			c.start("ember", turns, nil)
			c.say("switch to Mira", "What is the code?")
			got := c.hostedNotices()
			if tc.notice == "" {
				if len(got) != 0 {
					t.Fatalf("a saved notice was repeated for the same share: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].message != tc.notice || got[0].claudeRequests != 0 {
				t.Fatalf("notices = %+v, want %q before the request", got, tc.notice)
			}
		})
	}
}

func TestHostedNoticeInterruptedBeforeTheRequestIsGivenAgain(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	c.say("Hello.", "switch to Mira")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stop := events.SubscribeDetachable(c.p.Events, func(e events.Info) {
		if strings.Contains(e.Message, "a hosted service") {
			cancel()
		}
	})
	if err := c.p.RunTurnTextMode(ctx, "What do you think?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	stop()
	if _, err := os.Stat(c.log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a request reached Claude after the stopped notice: %v", err)
	}
	if saved := c.saved(); len(saved.HostedNotified) != 0 {
		t.Fatalf("a notice whose turn stopped before anything was sent was saved as given: %q", saved.HostedNotified)
	}

	c.say("What do you think?")
	got := c.hostedNotices()
	if len(got) != 2 || got[1].message != miraNotice || got[1].claudeRequests != 0 {
		t.Fatalf("notices = %+v, want the notice again before the first request", got)
	}
	if saved := c.saved(); !slices.Equal(saved.HostedNotified, []string{"claude"}) {
		t.Fatalf("saved notices = %q", saved.HostedNotified)
	}
}

func TestHostedNoticeComesBeforeCompactionIsTheFirstRequest(t *testing.T) {
	c := newClaudeConversation(t, 20)
	c.say("Note that the vault code is cobalt.", "The door is teal.")
	saved := c.saved()
	c.sess = saved
	c.sess.HostedNotified = nil
	c.start("mira", saved.Turns, saved.PersonaContexts)
	before, requests := len(c.hostedNotices()), len(readClaudeRequests(t, c.log))

	c.p.CompactPrompt = "Summarize the conversation."
	if err := c.p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(readClaudeRequests(t, c.log)); n != requests+1 {
		t.Fatalf("compaction made %d requests", n-requests)
	}
	got := c.hostedNotices()
	if len(got) != before+1 || got[before].claudeRequests != requests {
		t.Fatalf("notices = %+v, want one before the compaction request (request %d)", got, requests)
	}
	c.say("Where were we?")
	if got := c.hostedNotices(); len(got) != before+1 {
		t.Fatalf("the notice was repeated after compaction: %+v", got)
	}
}

func TestHostedNoticeComesBeforeAHandoffToAHostedPersona(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.p.RouteHandoff = c.selector.RouteHandoff
	c.mu.Lock()
	c.emberSays = "@Mira what color do you think the door is?"
	c.mu.Unlock()
	c.say("Note that the door is teal.")
	got := c.hostedNotices()
	if len(got) != 1 || got[0].message != miraNotice || got[0].claudeRequests != 0 {
		t.Fatalf("notices = %+v, want %q before the handoff request", got, miraNotice)
	}
	if n := len(readClaudeRequests(t, c.log)); n != 1 {
		t.Fatalf("claude requests = %d", n)
	}
}

func TestOnlyWholeConversationSharingSendsEarlierLines(t *testing.T) {
	fromEnv := func(value string) func(t *testing.T) string {
		return func(t *testing.T) string {
			t.Setenv("HOSTED_PERSONA_SHARING", value)
			config.SetConfigDirForTest(t, t.TempDir())
			loaded, err := config.LoadWithoutHooks()
			if err != nil {
				t.Fatal(err)
			}
			return loaded.HostedPersonaSharing
		}
	}
	fixed := func(value string) func(*testing.T) string { return func(*testing.T) string { return value } }
	for _, tc := range []struct {
		name    string
		sharing func(*testing.T) string
		whole   bool
	}{
		{"empty", fixed(""), false},
		{"from_invite", fixed("from_invite"), false},
		{"whole_conversation", fixed("whole_conversation"), true},
		{"unrecognised", fixed("bogus"), false},
		{"environment whole_conversation", fromEnv("whole_conversation"), true},
		{"environment unrecognised", fromEnv("bogus"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMixedConversation(t, 20, "ember")
			c.base.HostedPersonaSharing = tc.sharing(t)
			c.start("ember", nil, nil)
			c.say("The vault code is cobalt.", "switch to Mira", "What is the code?")
			prompt := readClaudeRequests(t, c.log)[0].Prompt
			cursor := c.saved().PersonaContexts["mira"]
			notices := c.hostedNotices()
			if len(notices) != 1 || cursor.Session == nil {
				t.Fatalf("notices = %+v, cursor = %+v", notices, cursor)
			}
			if tc.whole {
				requireContains(t, prompt, "The vault code is cobalt.")
				if cursor.Session.From != 0 || !strings.Contains(notices[0].message, "The whole conversation is sent to Claude.") {
					t.Fatalf("cursor = %+v, notice = %q", cursor, notices[0].message)
				}
				return
			}
			requireOmits(t, prompt, "The vault code is cobalt.")
			if cursor.Session.From == 0 || notices[0].message != miraNotice {
				t.Fatalf("cursor = %+v, notice = %q", cursor, notices[0].message)
			}
		})
	}
}
