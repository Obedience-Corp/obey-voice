package brain

import (
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/ollama/ollama/api"
)

const cannotViewNote = "(desk.png): not shown, because this model cannot view images."

var askBothWays = map[string]func(*testing.T, *OllamaBrain, string, StreamOptions) error{
	"blocking": func(t *testing.T, o *OllamaBrain, input string, opts StreamOptions) error {
		_, err := o.ThinkFull(t.Context(), input, opts)
		return err
	},
	"streaming": func(t *testing.T, o *OllamaBrain, input string, opts StreamOptions) error {
		stream, err := o.ThinkStream(t.Context(), input, opts)
		if err != nil {
			return err
		}
		drainStream(t, stream)
		return (<-stream.Done).Err
	},
}

// sharedPicture is a conversation in which a model that views images has
// answered a turn with one picture.
func sharedPicture(t *testing.T) (*OllamaBrain, chatmedia.Ref) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 1, 32, 32)
	refs[0].Name = "desk.png"
	viewer := (&visionFake{}).brain(t, budgetConfig(), "You are Obey.")
	if _, err := viewer.ThinkFull(t.Context(), "What is on my screen?", StreamOptions{Attachments: refs}); err != nil {
		t.Fatal(err)
	}
	return viewer, refs[0]
}

// assertNoPictureSent checks every request of a text-only model: no image
// bytes, and one line saying the picture was left out.
func assertNoPictureSent(t *testing.T, fake *visionFake, want int) {
	t.Helper()
	sent := fake.sent()
	if len(sent) != want {
		t.Fatalf("model received %d requests, want %d", len(sent), want)
	}
	for i, req := range sent {
		notes := 0
		for _, m := range req.Messages {
			if len(m.Images) > 0 {
				t.Fatalf("request %d sent an image to a model that cannot view one", i)
			}
			notes += strings.Count(m.Content, cannotViewNote)
			if strings.Contains(m.Content, "no longer attached") {
				t.Fatalf("request %d describes the picture twice: %q", i, m.Content)
			}
		}
		if notes != 1 {
			t.Fatalf("request %d names the withheld picture %d times, want once", i, notes)
		}
	}
}

func TestTextOnlyModelAnswersAConversationThatHoldsAPicture(t *testing.T) {
	// A restored session and a persona switch load the conversation differently.
	loads := map[string]func(*OllamaBrain, *OllamaBrain){
		"restore": func(o, from *OllamaBrain) { o.LoadHistory(from.History()) },
		"switch":  func(o, from *OllamaBrain) { _ = o.ImportContinuity(from.ContinuitySnapshot()) },
	}
	for name, ask := range askBothWays {
		for loaded, load := range loads {
			t.Run(name+" after "+loaded, func(t *testing.T) {
				viewer, ref := sharedPicture(t)
				fake := &visionFake{textOnly: true}
				o := fake.brain(t, budgetConfig(), "You are Venessa.")
				load(o, viewer)
				var told []string
				opts := StreamOptions{OnImagesWithheld: func(model string, refs []chatmedia.Ref) {
					for _, ref := range refs {
						told = append(told, model+" "+ref.Name)
					}
				}}
				if err := ask(t, o, "Venessa, what do you think?", opts); err != nil {
					t.Fatal(err)
				}
				afterOne := o.History()
				// The picture ages past the image window during these turns.
				for _, input := range []string{"Go on.", "And then?", "Anything else?"} {
					if err := ask(t, o, input, opts); err != nil {
						t.Fatalf("%q: %v", input, err)
					}
				}
				assertNoPictureSent(t, fake, 4)
				assertWorkingAligned(t, o)
				// Reported while a model that views images would still be sent it.
				if !slices.Equal(told, []string{"fixture desk.png", "fixture desk.png"}) {
					t.Fatalf("reported withheld pictures %q", told)
				}

				if first := afterOne[0]; first.Content != "What is on my screen?" || len(first.Attachments) != 1 || first.Attachments[0] != ref {
					t.Fatalf("the conversation record lost the picture: %+v", first)
				}
				again := &visionFake{}
				back := again.brain(t, budgetConfig(), "You are Obey.")
				back.LoadHistory(afterOne)
				if err := ask(t, back, "Look at it once more.", StreamOptions{}); err != nil {
					t.Fatal(err)
				}
				if got := imageTurnsIn(again.sent()[0].Messages); len(got) != 1 {
					t.Fatalf("a model that views images received %d image turns, want 1", len(got))
				}
			})
		}
	}
}

// A turn already admitted with a picture reaches a text-only model when the
// user addresses that persona by name, or the group answers in turn.
func TestTextOnlyModelKeepsANewPictureOnTheRecord(t *testing.T) {
	for name, ask := range askBothWays {
		t.Run(name, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			refs := photos(t, 1, 32, 32)
			refs[0].Name = "desk.png"
			fake := &visionFake{textOnly: true}
			o := fake.brain(t, budgetConfig(), "You are Venessa.")
			if err := ask(t, o, "Venessa, what is on my screen?", StreamOptions{Attachments: refs}); err != nil {
				t.Fatal(err)
			}
			if err := ask(t, o, "Go on.", StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			assertNoPictureSent(t, fake, 2)
			assertWorkingAligned(t, o)
			if first := o.History()[0]; first.Content != "Venessa, what is on my screen?" || len(first.Attachments) != 1 || first.Attachments[0] != refs[0] {
				t.Fatalf("the conversation record lost the picture: %+v", first)
			}
		})
	}
}

// The note is for the model. What the user said stays the text that the
// archive keeps and that an archive lookup reads.
func TestPictureNoteIsNotReadAsTheUsersWords(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 1, 32, 32)
	refs[0].Name = "earlier-draft.png"
	fake := &visionFake{textOnly: true}
	o := fake.brain(t, budgetConfig(), "You are Venessa.")
	o.archive.Add(continuity.Entry{Kind: "tool", Summary: "an unrelated archived note"})
	if _, err := o.ThinkFull(t.Context(), "Venessa, what is this?", StreamOptions{Attachments: refs}); err != nil {
		t.Fatal(err)
	}
	last := lastUserIndex(o.history)
	if !strings.Contains(o.history[last].Content, "(earlier-draft.png): not shown") {
		t.Fatalf("the picture turn reads %q", o.history[last].Content)
	}
	o.history, o.working = o.history[:last+1], o.working[:last+1]
	if notes := o.recallNotes(); notes != "" {
		t.Fatalf("a file name recalled archived notes: %q", notes)
	}
	o.archiveTurns(o.history, o.working)
	entries := o.ContinuityArchive()
	if note := entries[len(entries)-1].Summary; note != "user: Venessa, what is this?\n" {
		t.Fatalf("archive note = %q", note)
	}
}

// No user message keeps picture bytes, even one the working view has lost.
func TestTextOnlyModelIsSentNoPictureWhenTheViewsDisagree(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 1, 32, 32)
	fake := &visionFake{textOnly: true}
	o := fake.brain(t, budgetConfig(), "You are Venessa.")
	o.history = []api.Message{
		{Role: "user", Content: "Look.", Images: []api.ImageData{{1}}},
		{Role: "assistant", Content: "Seen."},
		{Role: "user", Content: "And this.", Images: []api.ImageData{{2}}},
	}
	o.working = []Turn{{Role: "user", Content: "Look.", Attachments: refs}}
	if err := o.prepareImageHistory(t.Context(), StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := imageTurnsIn(o.history); len(got) != 0 {
		t.Fatalf("user messages %v still carry picture bytes", got)
	}
}

// Muse Spark and Obey take no image input. Their record of a picture turn
// names the file and says the model was not shown it.
func TestProvidersWithoutImageInputAreToldAPictureWasNotShown(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "What is on my screen?", Attachments: []chatmedia.Ref{{ID: "abc", Name: "desk.png", Format: "png"}}},
		{Role: "assistant", Content: "A terminal.", PersonaID: "obey", PersonaDisplayName: "Obey"},
	}
	for name, b := range map[string]interface {
		Provider
		buildPrompt(string, StreamOptions) string
	}{"muse-spark": testMuse(&fakeMuse{}), "obey": obeyTestBrain(nil)} {
		t.Run(name, func(t *testing.T) {
			b.LoadHistory(turns)
			prompt := b.buildPrompt("What do you think?", StreamOptions{})
			if got := strings.Count(prompt, "Attached image abc (desk.png): not shown, because this model cannot view images."); got != 1 {
				t.Fatalf("the prompt says the picture was not shown %d times:\n%s", got, prompt)
			}
			if history := b.History(); history[0].Content != "What is on my screen?" || len(history[0].Attachments) != 1 {
				t.Fatalf("the record changed: %+v", history[0])
			}
		})
	}
}
