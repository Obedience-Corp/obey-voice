package personaruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// hostedShareProviders are the hosted backends that keep the shared history.
var hostedShareProviders = []string{"codex", "muse-spark", "obey"}

var providerLabelFor = map[string]string{"codex": "Codex", "muse-spark": "Muse Spark", "obey": "Obey"}

// miraReplyOn is what each provider's fixture answers.
var miraReplyOn = map[string]string{"codex": "I remember cobalt.", "muse-spark": scoutReply, "obey": scoutReply}

const (
	preInviteUser  = "Remember PRIVATE-4711 for me."
	preInviteEmber = "PRE-INVITE: noted privately."
	awayEmber      = "AWAY: Ember answered."
)

// hostedPrompts returns the text of each request Mira's provider received.
func (c *claudeConversation) hostedPrompts() []string {
	c.t.Helper()
	var out []string
	if c.provider != "codex" {
		for _, p := range readFixturePrompts(c.t, c.log) {
			out = append(out, p.Prompt)
		}
		return out
	}
	data, err := os.ReadFile(c.log)
	if err != nil {
		c.t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		var m struct {
			Method string
			Params struct{ Input []struct{ Text string } }
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			c.t.Fatal(err)
		}
		if m.Method != "turn/start" {
			continue
		}
		var text strings.Builder
		for _, in := range m.Params.Input {
			text.WriteString(in.Text + "\n")
		}
		out = append(out, text.String())
	}
	return out
}

// hostedLog is everything Mira's provider was sent, as it was sent.
func (c *claudeConversation) hostedLog() string {
	data, _ := os.ReadFile(c.log)
	return string(data)
}

func (c *claudeConversation) lastHostedPrompt() string {
	c.t.Helper()
	prompts := c.hostedPrompts()
	if len(prompts) == 0 {
		c.t.Fatal("no requests to the hosted provider")
	}
	return prompts[len(prompts)-1]
}

func (c *claudeConversation) setEmberReply(text string) {
	c.mu.Lock()
	c.emberSays = text
	c.mu.Unlock()
}

// emberUsesATool records an exchange in which Ember, live on Ollama, reads an
// attached image with a tool, and returns the attachment.
func (c *claudeConversation) emberUsesATool() chatmedia.Ref {
	c.t.Helper()
	config.SetConfigDirForTest(c.t, c.t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("vault-scan.png", testmedia.PNG())
	if err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.vision = true
	c.mu.Unlock()
	at := time.Now().UTC()
	exchange := []brain.Turn{
		{Role: "user", Content: "Read the vault scan.", Attachments: []chatmedia.Ref{ref}, At: at},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []brain.TurnToolCall{{ID: "call-SECRET-ID", Name: "vault_reader", Arguments: `{"path":"~/private/diary.md"}`}}, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e-tool", At: at.Add(time.Millisecond)},
		{Role: "tool", ToolName: "vault_reader", ToolCallID: "call-SECRET-ID", Content: "TOOL OUTPUT SECRET", At: at.Add(2 * time.Millisecond)},
		{Role: "assistant", Content: "Your notes say hi.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e-tool", At: at.Add(3 * time.Millisecond)},
	}
	ember, ok := c.p.Brain.(brain.ContinuityTransferProvider)
	if !ok || c.selector.Snapshot().PersonaID != "ember" {
		c.t.Fatal("Ember is not answering")
	}
	snapshot := ember.ContinuitySnapshot()
	snapshot.Canonical = append(snapshot.Canonical, exchange...)
	snapshot.Working = append(snapshot.Working, exchange...)
	if err := ember.ImportContinuity(snapshot); err != nil {
		c.t.Fatal(err)
	}
	return ref
}

// requireEmberHasEverything checks Ember's last request: the whole
// conversation, its own tool traffic, and Mira's lines attributed to Mira.
func (c *claudeConversation) requireEmberHasEverything() {
	c.t.Helper()
	requests := c.ollamaRequests()
	var sent strings.Builder
	for _, m := range requests[len(requests)-1] {
		sent.WriteString(m.Role + ": " + m.Content + " " + string(m.ToolCalls) + "\n")
		if m.Role == "assistant" && strings.Contains(m.Content, miraReplyOn[c.provider]) {
			c.t.Fatalf("Mira's reply was handed to Ember as its own:\n%s", sent.String())
		}
	}
	requireContains(c.t, sent.String(), preInviteUser, preInviteEmber, "Read the vault scan.", "vault_reader", "TOOL OUTPUT SECRET", "Your notes say hi.", miraReplyOn[c.provider])
}

func TestHostedSharedHistorySendsOnlyTheShareWithoutOtherPersonasToolTraffic(t *testing.T) {
	for _, provider := range hostedShareProviders {
		for _, sharing := range []string{"", "whole_conversation"} {
			whole := sharing == "whole_conversation"
			t.Run(provider+"/"+sharing, func(t *testing.T) {
				c := newConversation(t, 20, "ember", provider, sharing)
				c.setEmberReply(preInviteEmber)
				c.say(preInviteUser)
				c.say("switch to Mira", "What do you think?")
				c.setEmberReply(awayEmber)
				c.say("switch to Ember", "Check my notes.")
				ref := c.emberUsesATool()
				c.say("switch to Mira", "What did Ember find?")

				prompts := c.hostedPrompts()
				if len(prompts) != 2 {
					t.Fatalf("%s requests = %d", provider, len(prompts))
				}
				requireContains(t, prompts[0], "switch to Mira", "Mira here.", "What do you think?")
				// What was said while Mira was away reaches her on her return.
				requireContains(t, prompts[1], "Check my notes.", awayEmber, "Read the vault scan.", "Let me look.", "Your notes say hi.", "What did Ember find?")
				for _, p := range prompts {
					requireOmits(t, p, "vault_reader", "call-SECRET-ID", "diary.md", "TOOL OUTPUT SECRET", "vault-scan.png", ref.ID)
					if whole {
						requireContains(t, p, preInviteUser, preInviteEmber)
					} else {
						requireOmits(t, p, "PRIVATE-4711", "PRE-INVITE")
					}
				}
				log := c.hostedLog()
				requireOmits(t, log, "vault_reader", "call-SECRET-ID", "diary.md", "TOOL OUTPUT SECRET", "vault-scan.png", ref.ID)
				if !whole {
					requireOmits(t, log, "PRIVATE-4711", "PRE-INVITE")
				}

				label := providerLabelFor[provider]
				want := "Mira runs on " + label + ", a hosted service. Only what is said from when it was invited is sent to " + label + "; earlier lines stay on this machine."
				if whole {
					want = "Mira runs on " + label + ", a hosted service. The whole conversation is sent to " + label + "."
				}
				if notices := c.hostedNotices(); len(notices) != 1 || notices[0].message != want {
					t.Fatalf("notices = %+v, want %q", notices, want)
				}

				c.say("switch to Ember", "Anything else?")
				c.requireEmberHasEverything()
				transcript := c.selector.Transcript()
				var kept bool
				for _, turn := range transcript {
					kept = kept || len(turn.ToolCalls) == 1 && turn.ToolCalls[0].Name == "vault_reader"
				}
				if !kept || !strings.Contains(transcript[0].Content, "PRIVATE-4711") {
					t.Fatalf("the canonical record lost lines: %+v", transcript)
				}
			})
		}
	}
}

func TestHostedShareStartSurvivesSaveAndResume(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.setEmberReply(preInviteEmber)
			c.say(preInviteUser, "switch to Mira", "What do you think?")
			saved := c.saved()
			cursor := saved.PersonaContexts["mira"]
			if cursor.Session == nil || cursor.Session.From != 2 || cursor.Session.Provider != provider || len(cursor.Working) == 0 {
				t.Fatalf("saved cursor = %+v", cursor)
			}

			c.start("mira", saved.Turns, saved.PersonaContexts)
			c.say("Still there?")
			requireContains(t, c.lastHostedPrompt(), "What do you think?", miraReplyOn[provider], "Still there?")

			saved = c.saved()
			c.start("ember", saved.Turns, saved.PersonaContexts)
			c.say("switch to Mira", "And now?")
			requireContains(t, c.lastHostedPrompt(), "What do you think?", "Still there?", "And now?")
			requireOmits(t, c.hostedLog(), "PRIVATE-4711", "PRE-INVITE")
			c.say("switch to Ember", "Your turn.")
			ollama := c.ollamaRequests()
			var sent strings.Builder
			for _, m := range ollama[len(ollama)-1] {
				sent.WriteString(m.Content + "\n")
			}
			requireContains(t, sent.String(), preInviteUser, preInviteEmber, "Still there?", "And now?")
		})
	}
}

func TestPersonaMovedOntoAHostedSharedHistorySendsNothingFromItsLocalTurns(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.say("My private note: the safe code is PRIVATE-4711.", "Thanks.")
			ember := testProfile("ember", "Ember", "ember")
			ember.Brain.Provider, ember.Brain.Model = provider, ""
			writeTestProfile(t, c.catalog.Dir, ember)
			c.say("What do you think now?")
			if cursor := c.saved().PersonaContexts["ember"]; cursor.Session == nil || cursor.Session.From != 4 || cursor.Session.Provider != provider {
				t.Fatalf("saved cursor = %+v", cursor)
			}

			saved := c.saved()
			c.start("ember", saved.Turns, saved.PersonaContexts)
			c.say("And after a restart?")
			prompts := c.hostedPrompts()
			if len(prompts) != 2 {
				t.Fatalf("requests = %d", len(prompts))
			}
			requireContains(t, prompts[1], "What do you think now?", "And after a restart?")
			requireOmits(t, c.hostedLog(), "PRIVATE-4711", "Thanks.")
		})
	}
}

func TestSinglePersonaConversationOnAHostedSharedHistoryKeepsItsWholeConversation(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "mira", provider, "")
			c.say("Remember the code K-77.", "What was it?")
			label := providerLabelFor[provider]
			if notices := c.hostedNotices(); len(notices) != 1 || notices[0].message != "Mira runs on "+label+", a hosted service. What is said to it is sent to "+label+"." {
				t.Fatalf("notices = %+v", notices)
			}
			if provider != "codex" {
				// Codex continues its thread; the others are sent the replay.
				requireContains(t, c.lastHostedPrompt(), "Remember the code K-77.", miraReplyOn[provider], "What was it?")
			}

			saved := c.saved()
			if cursor := saved.PersonaContexts["mira"]; cursor.Session == nil || cursor.Session.From != 0 || len(cursor.Working) != 4 {
				t.Fatalf("saved cursor = %+v", cursor)
			}
			c.start("mira", saved.Turns, saved.PersonaContexts)
			c.say("And now?")
			requireContains(t, c.lastHostedPrompt(), "Remember the code K-77.", "What was it?", miraReplyOn[provider], "And now?")

			// Replies name their persona, so a conversation saved without a
			// share start does not count as inherited: nothing earlier is sent.
			saved = c.saved()
			c.start("mira", saved.Turns, nil)
			c.say("Anything else?")
			requireOmits(t, c.lastHostedPrompt(), "K-77")
		})
	}
}

func TestHostedSharedHistoryReceivesOnlyResultsOfJobsItStartedOnItsBackend(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.work = true
			c.start("ember", nil, nil)
			coordinator := c.p.WorkCoordinator()
			scope := coordinator.Scope()
			start := func(id, persona, backend, task string) work.JobKey {
				t.Helper()
				key, err := coordinator.Register(scope, work.Admission{JobID: id, Task: task, Origin: "agency", Config: map[string]string{"persona": persona, "brain": backend}})
				if err != nil {
					t.Fatal(err)
				}
				if err := coordinator.Started(key); err != nil {
					t.Fatal(err)
				}
				return key
			}
			finish := func(key work.JobKey, text string) {
				t.Helper()
				if _, err := coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: text}); err != nil {
					t.Fatal(err)
				}
			}
			publish := func(id, persona, backend, text string) { finish(start(id, persona, backend, "task "+id), text) }
			emberJob := start("ember-job", "ember", "ollama", "Read my private tax notes")
			c.say("switch to Mira", "What do you think?")
			if cfg := c.p.Agency.WorkerMigrationConfig(); cfg.ActivePersona != "mira" || cfg.BrainProvider != provider {
				t.Fatalf("jobs would be recorded for %q on %q", cfg.ActivePersona, cfg.BrainProvider)
			}
			finish(emberJob, "The tax refund is 1234.")
			publish("mira-local-job", "mira", "ollama", "The local diary says 5678.")
			publish("mira-claude-job", "mira", "claude", "The Claude memo says 9012.")
			if c.p.BackgroundPending() {
				t.Fatal("a result Mira did not start on her current backend is offered to her")
			}
			publish("mira-job", "mira", provider, "The build is green.")
			if !c.p.BackgroundPending() {
				t.Fatal("Mira's own result is not offered to her")
			}
			c.say("And the build?")
			requireContains(t, c.lastHostedPrompt(), "The build is green.")
			requireOmits(t, c.hostedLog(), "1234", "5678", "9012", "tax refund", "tax notes", "diary", "Claude memo")
		})
	}
}

func TestJobStatusGivesAHostedPersonaOnlyACountOfJobsItDidNotStart(t *testing.T) {
	for _, provider := range append([]string{"claude"}, hostedShareProviders...) {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			binding, err := c.catalog.ResolveBinding(c.base, "ember")
			if err != nil {
				t.Fatal(err)
			}
			jobs := job.NewTable(t.Context())
			t.Cleanup(jobs.Close)
			pipeline.NewAgency(c.p, jobs, nil, binding.Config(), nil).Wire()
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			if _, err := jobs.Start(t.Context(), job.Spec{Task: "Read my private tax notes", Run: func(ctx context.Context) (string, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return "", ctx.Err()
			}}); err != nil {
				t.Fatal(err)
			}
			c.say("switch to Mira", "status", "What do you think?")
			requireOmits(t, c.hostedLog(), "tax notes")
			var said strings.Builder
			for _, turn := range c.selector.Transcript() {
				said.WriteString(turn.Content + "\n")
			}
			requireContains(t, said.String(), "One other job is running.")
		})
	}
}

func TestHostedPersonaSelectedButNeverAddressedIsSharedByTheSettingInForceWhenAddressed(t *testing.T) {
	for _, provider := range append([]string{"claude"}, hostedShareProviders...) {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "whole_conversation")
			c.setEmberReply(preInviteEmber)
			c.say(preInviteUser)
			for _, id := range []string{"mira", "ember"} {
				if _, err := c.selector.Select(t.Context(), id, "ui"); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(c.hostedLog(), "PRIVATE-4711") {
				t.Fatal("Mira was sent the conversation before being addressed")
			}
			narrowed := c.base.Clone()
			narrowed.HostedPersonaSharing = ""
			if err := c.selector.Reload(t.Context(), narrowed); err != nil {
				t.Fatal(err)
			}
			c.base = narrowed
			c.say("switch to Mira", "First words to you.")
			requireOmits(t, c.hostedLog(), "PRIVATE-4711", "PRE-INVITE")
			label := providerLabel(provider)
			want := "Mira runs on " + label + ", a hosted service. Only what is said from when it was invited is sent to " + label + "; earlier lines stay on this machine."
			if notices := c.hostedNotices(); len(notices) != 1 || notices[0].message != want {
				t.Fatalf("notices = %+v, want %q", notices, want)
			}
		})
	}
}

func TestHostedShareThatHasBeenSentSomethingKeepsItsStartWhenTheSettingChanges(t *testing.T) {
	for _, provider := range hostedShareProviders {
		for _, tc := range []struct{ name, from, to string }{
			{"narrowed", "whole_conversation", ""},
			{"widened", "", "whole_conversation"},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				c := newConversation(t, 20, "ember", provider, tc.from)
				c.setEmberReply(preInviteEmber)
				c.say(preInviteUser)
				c.setEmberReply(awayEmber)
				c.say("switch to Mira", "What do you think?", "switch to Ember", "Over to you.")
				changed := c.base.Clone()
				changed.HostedPersonaSharing = tc.to
				if err := c.selector.Reload(t.Context(), changed); err != nil {
					t.Fatal(err)
				}
				c.base = changed
				c.say("switch to Mira", "Back to you.")
				last := c.lastHostedPrompt()
				requireContains(t, last, "What do you think?", "Over to you.", "Back to you.")
				if tc.from == "whole_conversation" {
					requireContains(t, last, preInviteUser, preInviteEmber)
				} else {
					requireOmits(t, c.hostedLog(), "PRIVATE-4711", "PRE-INVITE")
				}
			})
		}
	}
}

func TestCompactingAHostedShareAddsNothingFromOutsideIt(t *testing.T) {
	for _, provider := range hostedShareProviders {
		for _, sharing := range []string{"", "whole_conversation"} {
			t.Run(provider+"/"+sharing, func(t *testing.T) {
				c := newConversation(t, 20, "ember", provider, sharing)
				c.setEmberReply(preInviteEmber)
				c.say(preInviteUser)
				ref := c.emberUsesATool()
				if _, err := c.selector.Select(t.Context(), "mira", "explicit"); err != nil {
					t.Fatal(err)
				}
				c.p.CompactPrompt = "Summarize the conversation."
				if err := c.p.CompactConversation(t.Context()); err != nil {
					t.Fatal(err)
				}
				c.say("What now?")
				requireOmits(t, c.hostedLog(), "vault_reader", "call-SECRET-ID", "diary.md", "TOOL OUTPUT SECRET", "vault-scan.png", ref.ID)
				if sharing == "" {
					requireOmits(t, c.hostedLog(), "PRIVATE-4711", "PRE-INVITE", "Read the vault scan.")
				}
			})
		}
	}
}

func TestLoadHistoryKeepsEveryTurnButGivesAHostedSharedHistoryOnlyItsShare(t *testing.T) {
	other := []brain.Turn{
		{Role: "user", Content: "Another conversation: the safe code is PRIVATE-4711."},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "other"},
	}
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "mira", provider, "")
			view := func() []brain.Turn { return c.p.Brain.(brain.ContextHistoryProvider).ContextHistory() }
			c.selector.LoadHistory(other)
			if got := view(); len(got) != 0 || len(c.p.Brain.History()) != len(other) {
				t.Fatalf("a persona never addressed is sent %+v; record = %+v", got, c.p.Brain.History())
			}
			c.selector.LoadHistory(nil)
			c.say("Note the door is teal.")
			own := c.selector.Transcript()
			c.selector.LoadHistory(other)
			if got := view(); len(got) != 0 {
				t.Fatalf("another conversation is sent: %+v", got)
			}
			// A host whose resume fails loads the previous conversation again.
			c.selector.LoadHistory(own)
			if got := view(); len(got) != 2 || got[0].Content != "Note the door is teal." || len(c.p.Brain.History()) != 2 {
				t.Fatalf("the persona's own share = %+v", got)
			}
		})
	}
}

func TestPersonaOnTheSameHostedBackendIsSentOnlyItsOwnShare(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "mira", provider, "")
			nova := testProfile("nova", "Nova", "nova")
			nova.Brain.Provider, nova.Brain.Model = provider, ""
			writeTestProfile(t, c.catalog.Dir, nova)
			writeTestPrompt(t, c.base.PromptsDir, "nova", "You are {agent_name}.")
			c.say(preInviteUser)
			c.say("switch to Nova", "What do you know?")
			novaPrompt := c.lastHostedPrompt()
			requireContains(t, novaPrompt, "You are Nova.", "Nova here.", "What do you know?")
			requireOmits(t, novaPrompt, "PRIVATE-4711", `"persona":"Mira"`)

			c.say("switch to Mira", "Still with me?")
			requireContains(t, c.lastHostedPrompt(), "You are Mira.", preInviteUser, "What do you know?", `"persona":"Nova"`, "Still with me?")
			if transcript := c.selector.Transcript(); len(transcript) != 10 || transcript[0].Content != preInviteUser {
				t.Fatalf("the canonical record lost lines: %+v", transcript)
			}
		})
	}
}

func TestCodexPersonaKeepsTheImagesAttachedForIt(t *testing.T) {
	c := newConversation(t, 20, "mira", "codex", "")
	config.SetConfigDirForTest(t, t.TempDir())
	ref, err := chatmedia.DefaultStore().Import("diagram.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.vision = true
	c.mu.Unlock()
	if err := c.p.RunTurnTextMode(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}), "What is in this diagram?"); err != nil {
		t.Fatal(err)
	}
	attached := "Attached image " + ref.ID + " (diagram.png):"
	requireContains(t, c.lastHostedPrompt(), attached)

	saved := c.saved()
	c.start("mira", saved.Turns, saved.PersonaContexts)
	c.say("And now?")
	requireContains(t, c.lastHostedPrompt(), attached, "What is in this diagram?", "And now?")

	c.say("switch to Ember", "Hello Ember.", "switch to Mira", "Back to you.")
	requireContains(t, c.lastHostedPrompt(), attached, "Hello Ember.", "Back to you.")
}

func TestShareViewSendsNothingFromACursorThatNoLongerMatches(t *testing.T) {
	turns := []brain.Turn{
		{Role: "user", Content: preInviteUser},
		{Role: "assistant", Content: preInviteEmber, PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "e1"},
		{Role: "user", Content: "Mira, what do you think?"},
		{Role: "assistant", Content: "Mira here.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
	}
	replaced := brain.CloneTurns(turns)
	replaced[0].Content = "A different conversation."
	held := brain.SessionContext(replaced, brain.SessionCursor{From: 2, Provider: "codex"}, len(replaced))
	held.Working = brain.CloneTurns(replaced[2:])
	unheld := brain.SessionContext(replaced, brain.SessionCursor{From: 0, Provider: "codex"}, 2)
	longer := brain.SessionContext(append(brain.CloneTurns(turns), brain.Turn{Role: "user", Content: "Later."}), brain.SessionCursor{From: 4, Provider: "codex"}, 5)
	longer.Working = []brain.Turn{}
	for name, cursor := range map[string]brain.PersonaContext{"held view": held, "no held view": unheld, "longer conversation": longer} {
		t.Run(name, func(t *testing.T) {
			view := shareView(cursor, turns)
			if len(view.Working) != 0 || len(view.Canonical) != len(turns) {
				t.Fatalf("a cursor that does not match the conversation opened its share: %+v", view.Working)
			}
		})
	}
}

func TestLoadHistoryUsesOnlyACursorOfTheLiveBackend(t *testing.T) {
	turns := []brain.Turn{
		{Role: "user", Content: preInviteUser},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "Mira", ResponseID: "m1"},
	}
	for _, provider := range append([]string{"claude"}, hostedShareProviders...) {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "mira", provider, "")
			other := "claude"
			if provider == "claude" {
				other = "codex"
			}
			foreign := brain.SessionContext(turns, brain.SessionCursor{From: 0, Provider: other}, len(turns))
			c.selector.transcript.setLive("mira", &foreign)
			c.selector.LoadHistory(turns)
			var sent []brain.Turn
			if view, ok := c.p.Brain.(brain.ContextHistoryProvider); ok && provider != "claude" {
				sent = view.ContextHistory()
			} else {
				sent = c.p.Brain.History()
			}
			if len(sent) != 0 {
				t.Fatalf("a cursor from %s opened the share on %s: %+v", other, provider, sent)
			}
			if provider == "claude" {
				return
			}
			label := providerLabelFor[provider]
			if got, want := c.selector.noticeScope("mira", provider, label), "Only what is said from when it was invited is sent to "+label+"; earlier lines stay on this machine."; got != want {
				t.Fatalf("notice = %q, want %q", got, want)
			}
		})
	}
}

func TestSavedCursorWithoutABackendIsNotTakenUpOnCodexMuseSparkOrObey(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.say("switch to Mira", "What do you think?", "switch to Ember")
			saved := c.saved()
			cursor := saved.PersonaContexts["mira"]
			if cursor.Session == nil || cursor.Session.Provider != provider {
				t.Fatalf("saved cursor = %+v", cursor)
			}
			// Saved before cursors named their backend, when only Claude kept one.
			cursor.Session.Provider = ""
			saved.PersonaContexts["mira"] = cursor
			c.start("ember", saved.Turns, saved.PersonaContexts)
			c.say("switch to Mira", "And now?")
			last := c.lastHostedPrompt()
			requireContains(t, last, "And now?")
			requireOmits(t, last, "What do you think?")
			if after := c.saved().PersonaContexts["mira"]; after.Session == nil || after.Session.Provider != provider || after.Session.From <= cursor.Session.From {
				t.Fatalf("cursor after = %+v", after)
			}
		})
	}
}

func TestCompactedHostedShareComesBackAfterSaveAndResume(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "mira", provider, "")
			c.say("Remember the code K-77.", "Second thought.", "Third line.")
			c.p.CompactPrompt = "Summarize the conversation."
			if err := c.p.CompactConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			view := func() []brain.Turn { return c.p.Brain.(brain.ContextHistoryProvider).ContextHistory() }
			compacted := view()
			if len(compacted) == 0 || compacted[0].Role != "assistant" || strings.Contains(fmt.Sprint(compacted), "K-77") {
				t.Fatalf("compacted view = %+v", compacted)
			}
			saved := c.saved()
			if got := saved.PersonaContexts["mira"].Working; fmt.Sprint(contents(got)) != fmt.Sprint(contents(compacted)) {
				t.Fatalf("saved view = %v, want %v", contents(got), contents(compacted))
			}
			c.start("mira", saved.Turns, saved.PersonaContexts)
			if got := view(); fmt.Sprint(contents(got)) != fmt.Sprint(contents(compacted)) {
				t.Fatalf("resumed view = %v, want %v", contents(got), contents(compacted))
			}
			c.say("And now?")
			last := c.lastHostedPrompt()
			requireContains(t, last, compacted[0].Content, "Third line.", "And now?")
			requireOmits(t, last, "K-77")
		})
	}
}

func contents(turns []brain.Turn) []string {
	out := make([]string, len(turns))
	for i, t := range turns {
		out[i] = t.Role + ": " + t.Content
	}
	return out
}

// spokenLine is the group prompt's line naming who has replied in the thread.
func spokenLine(t *testing.T, prompt string) string {
	t.Helper()
	for line := range strings.Lines(prompt) {
		if i := strings.Index(line, "Agents with replies recorded in this thread:"); i >= 0 {
			return line[i:]
		}
	}
	t.Fatalf("no spoken line in:\n%s", prompt)
	return ""
}

func TestHostedShareNamesWhoSpokeInItAfterItsViewIsSummarized(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			nova := testProfile("nova", "Nova", "nova")
			nova.Brain.Model = ""
			writeTestProfile(t, c.catalog.Dir, nova)
			writeTestPrompt(t, c.base.PromptsDir, "nova", "You are {agent_name}.")
			c.say(preInviteUser, "switch to Mira", "What do you think?", "switch to Nova", "Your view?", "switch to Mira", "And yours?")
			before := spokenLine(t, c.lastHostedPrompt())
			requireContains(t, before, "Nova")
			requireOmits(t, before, "Ember")

			c.p.CompactPrompt = "Summarize the conversation."
			if err := c.p.CompactConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			c.say("Who spoke?")
			after := spokenLine(t, c.lastHostedPrompt())
			requireContains(t, after, "Nova")
			requireOmits(t, after, "Ember")

			saved := c.saved()
			c.start("mira", saved.Turns, saved.PersonaContexts)
			c.say("After a restart?")
			resumed := spokenLine(t, c.lastHostedPrompt())
			requireContains(t, resumed, "Nova")
			requireOmits(t, resumed, "Ember")
		})
	}
}
