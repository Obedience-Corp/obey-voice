package personaruntime

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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

func TestHostedSharedHistoryReceivesOnlyResultsOfJobsItStarted(t *testing.T) {
	for _, provider := range hostedShareProviders {
		t.Run(provider, func(t *testing.T) {
			c := newConversation(t, 20, "ember", provider, "")
			c.work = true
			c.start("ember", nil, nil)
			coordinator := c.p.WorkCoordinator()
			scope := coordinator.Scope()
			publish := func(id, persona, backend, text string) {
				t.Helper()
				key, err := coordinator.Register(scope, work.Admission{JobID: id, Task: "task " + id, Origin: "agency", Config: map[string]string{"persona": persona, "brain": backend}})
				if err != nil {
					t.Fatal(err)
				}
				if err := coordinator.Started(key); err != nil {
					t.Fatal(err)
				}
				if _, err := coordinator.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: text}); err != nil {
					t.Fatal(err)
				}
			}
			c.say("switch to Mira")
			if cfg := c.p.Agency.WorkerMigrationConfig(); cfg.ActivePersona != "mira" || cfg.BrainProvider != provider {
				t.Fatalf("jobs would be recorded for %q on %q", cfg.ActivePersona, cfg.BrainProvider)
			}
			publish("ember-job", "ember", "ollama", "The tax refund is 1234.")
			publish("mira-local-job", "mira", "ollama", "The local diary says 5678.")
			if c.p.BackgroundPending() {
				t.Fatal("a result Mira did not start on a hosted backend is offered to her")
			}
			publish("mira-job", "mira", provider, "The build is green.")
			if !c.p.BackgroundPending() {
				t.Fatal("Mira's own result is not offered to her")
			}
			c.say("And the build?")
			requireContains(t, c.lastHostedPrompt(), "The build is green.")
			requireOmits(t, c.hostedLog(), "1234", "5678", "tax refund", "diary")
		})
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
