package personaruntime

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// attachImage imports a distinct image under name. The attachment store
// lives under the config directory, so a test sets it once before any import.
func (c *claudeConversation) attachImage(name string, seed byte) chatmedia.Ref {
	c.t.Helper()
	data := testmedia.PNG()
	// Bytes after IEND leave the image valid and give each one its own id.
	data = append(slices.Clone(data), seed)
	ref, err := chatmedia.DefaultStore().Import(name, data)
	if err != nil {
		c.t.Fatal(err)
	}
	return ref
}

func (c *claudeConversation) sayWith(refs []chatmedia.Ref, input string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	defer cancel()
	if err := c.p.RunTurnTextMode(chatmedia.WithInput(ctx, refs), input); err != nil {
		c.t.Fatalf("%q: %v", input, err)
	}
}

func imageIDs(req claudeRequest) []string {
	var ids []string
	for _, img := range req.Images {
		ids = append(ids, img.SHA256)
	}
	return ids
}

func TestClaudePersonaReceivesTheImagesAttachedForIt(t *testing.T) {
	c := newClaudeConversation(t, 20)
	config.SetConfigDirForTest(t, t.TempDir())
	ref := c.attachImage("diagram.png", 1)
	c.sayWith([]chatmedia.Ref{ref}, "What is in this diagram?")
	first := lastClaudeRequest(t, c.log)
	if first.Input != "stream-json" || !slices.Equal(imageIDs(first), []string{ref.ID}) || first.Images[0].MediaType != "image/png" {
		t.Fatalf("image request = %+v", first)
	}
	requireContains(t, first.Prompt, "What is in this diagram?", "Attached image "+ref.ID+" (diagram.png):")

	c.say("And now?")
	if next := lastClaudeRequest(t, c.log); next.Input != "" || len(next.Images) != 0 || next.Resume == "" {
		t.Fatalf("a resumed session was sent the image again: %+v", next)
	}
	transcript := c.selector.Transcript()
	if len(transcript) != 4 || !slices.Equal(transcript[0].Attachments, []chatmedia.Ref{ref}) {
		t.Fatalf("the transcript lost the attachment: %+v", transcript)
	}
}

// TestClaudePersonaIsNeverSentAnotherPersonasImages gives Ember, on Ollama, an
// image before Mira on Claude is first addressed and another while Mira is
// parked, and Mira one of her own. Mira's return resumes her session and
// catches it up, or with a 100-token session fuse starts a fresh session that
// replays her own image only.
func TestClaudePersonaIsNeverSentAnotherPersonasImages(t *testing.T) {
	for _, sharing := range []string{"", "whole_conversation"} {
		for _, fuse := range []bool{false, true} {
			name := "sharing=" + sharing
			if fuse {
				name += "/fresh-session"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
				c := newConversation(t, 20, "ember", "claude", sharing)
				if fuse {
					c.base.ClaudeMaxSessionTokens = 100
				}
				c.start("ember", nil, nil)
				config.SetConfigDirForTest(t, t.TempDir())
				c.mu.Lock()
				c.vision = true
				c.mu.Unlock()
				early := c.attachImage("ember-early-secret.png", 1)
				own := c.attachImage("mira-diagram.png", 2)
				away := c.attachImage("ember-away-secret.png", 3)

				c.sayWith([]chatmedia.Ref{early}, "Ember, look at this early photo.")
				c.say("switch to Mira", "What do you think?")
				c.sayWith([]chatmedia.Ref{own}, "What is in this diagram?")
				c.say("switch to Ember")
				c.sayWith([]chatmedia.Ref{away}, "Ember, read this receipt.")
				c.say("switch to Mira", "What did Ember look at?")

				// Neither the bytes nor the name or id of Ember's images reach
				// Claude, whatever else of her lines does.
				requireNoEmberImage := func(stage string) {
					t.Helper()
					for _, req := range readClaudeRequests(t, c.log) {
						for _, id := range imageIDs(req) {
							if id == early.ID || id == away.ID {
								t.Fatalf("%s: Ember's image reached Claude: %+v", stage, req)
							}
						}
					}
					log, err := os.ReadFile(c.log)
					if err != nil {
						t.Fatal(err)
					}
					requireOmits(t, string(log), early.ID, away.ID, early.Name, away.Name)
					if sharing == "" {
						requireOmits(t, string(log), "early photo")
					}
				}
				requireNoEmberImage("before resume")

				requests := readClaudeRequests(t, c.log)
				if len(requests) != 3 {
					t.Fatalf("claude requests = %d", len(requests))
				}
				if !slices.Equal(imageIDs(requests[1]), []string{own.ID}) {
					t.Fatalf("Mira's image turn sent %q", imageIDs(requests[1]))
				}
				back := requests[2]
				if fuse {
					if back.Resume != "" || !slices.Equal(imageIDs(back), []string{own.ID}) {
						t.Fatalf("Mira's fresh session on return = %+v, want her own image only", back)
					}
				} else {
					if back.Resume == "" || len(back.Images) != 0 {
						t.Fatalf("Mira's resumed session on return = %+v, want no images", back)
					}
					requireContains(t, back.Prompt, "Ember, read this receipt.")
				}
				if sharing == "whole_conversation" && !fuse {
					requireContains(t, requests[0].Prompt, "Ember, look at this early photo.")
				}

				saved := c.saved()
				c.start("mira", saved.Turns, saved.PersonaContexts)
				c.say("Anything else?")
				requireNoEmberImage("after resume")
				if resumed := lastClaudeRequest(t, c.log); resumed.Resume != "" || len(resumed.Images) != 0 {
					t.Fatalf("the first request after resume = %+v", resumed)
				}

				for _, turns := range [][]brain.Turn{c.selector.Transcript(), c.saved().Turns} {
					var kept []chatmedia.Ref
					for _, turn := range turns {
						kept = append(kept, turn.Attachments...)
					}
					if !slices.Equal(kept, []chatmedia.Ref{early, own, away}) {
						t.Fatalf("the conversation record holds %+v", kept)
					}
				}
			})
		}
	}
}

func TestHostedNoticeComesBeforeAFirstRequestThatCarriesAnImage(t *testing.T) {
	c := newMixedConversation(t, 20, "ember")
	config.SetConfigDirForTest(t, t.TempDir())
	ref := c.attachImage("diagram.png", 1)
	c.say("Hello Ember.", "switch to Mira")
	c.sayWith([]chatmedia.Ref{ref}, "What is in this diagram?")
	notices := c.hostedNotices()
	if len(notices) != 1 || notices[0].message != miraNotice || notices[0].claudeRequests != 0 {
		t.Fatalf("notices = %+v, want one before any request to Claude", notices)
	}
	requests := readClaudeRequests(t, c.log)
	if len(requests) != 1 || !slices.Equal(imageIDs(requests[0]), []string{ref.ID}) {
		t.Fatalf("claude requests = %+v", requests)
	}
}

// TestClaudePersonaKeepsTalkingAfterAnEarlierImageIsDeleted removes Mira's
// image from the store. Her resumed session goes on without it, and a fresh
// one, which a 100-token session fuse starts on her return from Nova, is told
// it is no longer attached.
func TestClaudePersonaKeepsTalkingAfterAnEarlierImageIsDeleted(t *testing.T) {
	t.Setenv("OBEY_CLAUDE_FIXTURE_SESSIONS", "distinct")
	c := newMixedConversation(t, 20, "ember")
	c.base.ClaudeMaxSessionTokens = 100
	c.start("ember", nil, nil)
	c.addClaudePersona("nova", "Nova")
	config.SetConfigDirForTest(t, t.TempDir())
	ref := c.attachImage("diagram.png", 1)
	c.say("switch to Mira")
	c.sayWith([]chatmedia.Ref{ref}, "What is in this diagram?")
	if err := os.Remove(chatmedia.DefaultStore().Path(ref)); err != nil {
		t.Fatal(err)
	}

	c.say("And now?")
	if resumed := lastClaudeRequest(t, c.log); resumed.Resume == "" || len(resumed.Images) != 0 {
		t.Fatalf("request after the deletion = %+v", resumed)
	}
	c.say("switch to Nova", "Nova, tell me about the vault door please.", "switch to Mira", "Still with me?")
	back := lastClaudeRequest(t, c.log)
	if back.Resume != "" || back.Input != "stream-json" || len(back.Images) != 0 {
		t.Fatalf("Mira's fresh session = %+v", back)
	}
	requireContains(t, back.Prompt, "Attached image "+ref.ID+" (diagram.png): no longer attached")
	if transcript := c.selector.Transcript(); !slices.Equal(transcript[2].Attachments, []chatmedia.Ref{ref}) {
		t.Fatalf("the transcript lost the attachment: %+v", transcript[2])
	}
}
