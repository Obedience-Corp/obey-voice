package brain

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// postedPicture is a conversation in which the persona answered one turn and
// posted a picture with its reply.
func postedPicture(t *testing.T, fake *visionFake, refs ...chatmedia.Ref) (*OllamaBrain, chatmedia.Ref) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	ref := photos(t, 1, 32, 32)[0]
	ref.Name = "chart.png"
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	if _, err := o.ThinkFull(t.Context(), "Draw me a chart.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReplyMedia("Here is the chart.\nchart.png", append(refs, ref)); err != nil {
		t.Fatal(err)
	}
	return o, ref
}

func postedLine(ref chatmedia.Ref, by string) string {
	return "Attached image " + ref.ID + " (" + ref.Name + "): a picture " + by + " posted"
}

// imagesBy lists, for each message of a request that carries images, its
// role and text.
func imagesBy(t *testing.T, req api.ChatRequest) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, m := range req.Messages {
		if len(m.Images) == 0 {
			continue
		}
		if m.Role != "user" {
			t.Fatalf("a %s message carries %d images", m.Role, len(m.Images))
		}
		out[m.Content] += len(m.Images)
	}
	return out
}

func messageWith(t *testing.T, req api.ChatRequest, text string) api.Message {
	t.Helper()
	for _, m := range req.Messages {
		if strings.Contains(m.Content, text) {
			return m
		}
	}
	t.Fatalf("no message says %q", text)
	return api.Message{}
}

func TestPostedPictureRidesOnTheNextUserMessage(t *testing.T) {
	for name, ask := range askBothWays {
		t.Run(name, func(t *testing.T) {
			fake := &visionFake{}
			o, ref := postedPicture(t, fake)
			if err := ask(t, o, "What colour is it?", StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			req := fake.sent()[1]
			carrier := messageWith(t, req, "What colour is it?")
			want, err := chatmedia.DefaultStore().ModelImage(ref)
			if err != nil {
				t.Fatal(err)
			}
			if len(carrier.Images) != 1 || !bytes.Equal(carrier.Images[0], want) {
				t.Fatalf("the next user message carries %d images", len(carrier.Images))
			}
			if got := strings.Count(carrier.Content, postedLine(ref, "you")+"."); got != 1 {
				t.Fatalf("the picture is named %d times: %q", got, carrier.Content)
			}
			if len(imagesBy(t, req)) != 1 {
				t.Fatalf("images sent on %d messages, want 1", len(imagesBy(t, req)))
			}

			history := o.History()
			question, reply := history[len(history)-2], history[len(history)-3]
			if question.Content != "What colour is it?" || len(question.Attachments) != 0 {
				t.Fatalf("the user's turn now reads %+v", question)
			}
			if len(reply.Attachments) != 1 || reply.Attachments[0] != ref {
				t.Fatalf("the reply lost its picture: %+v", reply)
			}
			context := o.ContextHistory()
			if got := context[len(context)-2].Content; got != "What colour is it?" {
				t.Fatalf("the projected user turn reads %q", got)
			}
			assertWorkingAligned(t, o)
		})
	}
}

func TestPostedPictureAgesWithTheMessageThatCarriesIt(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	for _, input := range []string{"turn-2", "turn-3", "turn-4", "turn-5", "turn-6"} {
		if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	sent := fake.sent()[1:]
	for i, req := range sent {
		images := 0
		for _, n := range imagesBy(t, req) {
			images += n
		}
		lines := 0
		for _, m := range req.Messages {
			lines += strings.Count(m.Content, postedLine(ref, "you"))
		}
		if wantImages := map[bool]int{true: 1, false: 0}[i < imageTurns]; images != wantImages || lines != 1 {
			t.Fatalf("request for turn-%d carries %d images and names the picture %d times", i+2, images, lines)
		}
	}
	aged := messageWith(t, sent[len(sent)-1], "turn-2")
	if !strings.HasSuffix(aged.Content, postedLine(ref, "you")+", no longer attached.") {
		t.Fatalf("the aged message reads %q", aged.Content)
	}
	assertWorkingAligned(t, o)

	assertAgedOnLoad(t, o, "turn-2", postedLine(ref, "you")+", no longer attached.")
}

// assertAgedOnLoad checks that a restored session and a persona switch name
// an aged picture once, with no request and no picture bytes.
func assertAgedOnLoad(t *testing.T, o *OllamaBrain, carrier, line string) {
	t.Helper()
	for name, load := range map[string]func(*OllamaBrain){
		"restore": func(r *OllamaBrain) { r.LoadHistory(o.History()) },
		"switch":  func(r *OllamaBrain) { _ = r.ImportContinuity(o.ContinuitySnapshot()) },
	} {
		r := &OllamaBrain{cfg: budgetConfig()}
		load(r)
		for _, m := range r.history {
			if len(m.Images) > 0 {
				t.Fatalf("%s: a message carries %d images", name, len(m.Images))
			}
			if strings.Contains(m.Content, carrier) && strings.Count(m.Content, line) != 1 {
				t.Fatalf("%s: the aged message reads %q", name, m.Content)
			}
		}
	}
}

// The user's own picture and a posted one on the same message keep one note
// each through aging and through a model that cannot view them.
func TestPostedPictureBesideTheUsersOwn(t *testing.T) {
	for name, fake := range map[string]*visionFake{"vision": {}, "text only": {textOnly: true}} {
		t.Run(name, func(t *testing.T) {
			o, ref := postedPicture(t, fake)
			own := photos(t, 2, 32, 32)[1:]
			own[0].Name = "mine.png"
			if _, err := o.ThinkFull(t.Context(), "turn-2 compare with mine", StreamOptions{Attachments: own}); err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{"turn-3", "turn-4", "turn-5", "turn-6"} {
				if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			for i, req := range fake.sent()[1:] {
				m := messageWith(t, req, "turn-2 compare with mine")
				if n := strings.Count(m.Content, postedLine(ref, "you")); n != 1 {
					t.Fatalf("request %d names the posted picture %d times: %q", i, n, m.Content)
				}
				if n := strings.Count(m.Content, "(mine.png):"); n > 1 {
					t.Fatalf("request %d names the user's picture %d times: %q", i, n, m.Content)
				}
			}
			if got := o.History()[2]; got.Content != "turn-2 compare with mine" || len(got.Attachments) != 1 {
				t.Fatalf("the user's turn now reads %+v", got)
			}
			assertWorkingAligned(t, o)
		})
	}
}

func TestAnotherPersonasPictureIsNamedForIt(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := photos(t, 1, 32, 32)[0]
	cfg := budgetConfig()
	cfg.ActivePersona, cfg.AgentName = "obey", "Obey"
	fake := &visionFake{}
	o := fake.brain(t, cfg, "You are Obey.")
	turns := []Turn{
		{Role: "user", Content: "Venessa, draw the plan."},
		{Role: "assistant", Content: "Here it is.\nplan", PersonaID: "venessa", PersonaDisplayName: "Venessa", Attachments: []chatmedia.Ref{ref}},
	}
	if err := o.ImportContinuity(ContinuitySnapshot{Canonical: turns}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ThinkFull(t.Context(), "Obey, what do you make of it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	req := fake.sent()[0]
	carrier := messageWith(t, req, "what do you make of it?")
	if len(carrier.Images) != 1 || !strings.Contains(carrier.Content, postedLine(ref, "Venessa")+".") {
		t.Fatalf("the user's message carries %d images and reads %q", len(carrier.Images), carrier.Content)
	}
	if quoted := messageWith(t, req, "Venessa agent:"); len(quoted.Images) != 0 {
		t.Fatal("the quoted reply carries the picture")
	}
}

func TestTextOnlyModelIsSentNoPostedPicture(t *testing.T) {
	for name, ask := range askBothWays {
		t.Run(name, func(t *testing.T) {
			fake := &visionFake{textOnly: true}
			o, ref := postedPicture(t, fake)
			var told []chatmedia.Ref
			opts := StreamOptions{OnImagesWithheld: func(_ string, refs []chatmedia.Ref) { told = append(told, refs...) }}
			for _, input := range []string{"What colour is it?", "Go on."} {
				if err := ask(t, o, input, opts); err != nil {
					t.Fatal(err)
				}
			}
			for i, req := range fake.sent()[1:] {
				if len(imagesBy(t, req)) != 0 {
					t.Fatalf("request %d sent a picture to a model that cannot view one", i)
				}
				lines := 0
				for _, m := range req.Messages {
					lines += strings.Count(m.Content, postedLine(ref, "you")+", not shown, because this model cannot view images.")
				}
				if lines != 1 {
					t.Fatalf("request %d names the withheld picture %d times", i, lines)
				}
			}
			if len(told) != 0 {
				t.Fatalf("the user was told about pictures they did not attach: %v", told)
			}
			assertWorkingAligned(t, o)
		})
	}
}

func TestPostedPictureThatNoLongerLoadsIsLeftOut(t *testing.T) {
	for name, ask := range askBothWays {
		t.Run(name, func(t *testing.T) {
			fake := &visionFake{}
			gone := chatmedia.Ref{ID: strings.Repeat("ab", 32), Name: "gone.png", Format: "png", Width: 1, Height: 1}
			o, ref := postedPicture(t, fake, gone)
			if err := ask(t, o, "What colour is it?", StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			carrier := messageWith(t, fake.sent()[1], "What colour is it?")
			if len(carrier.Images) != 1 || !strings.Contains(carrier.Content, postedLine(gone, "you")+", no longer attached.") || !strings.Contains(carrier.Content, postedLine(ref, "you")+".") {
				t.Fatalf("the message carries %d images and reads %q", len(carrier.Images), carrier.Content)
			}
		})
	}
}

// A request that already carries its limit of images leaves a posted picture
// out, and carries it again once there is room.
func TestPostedPictureCountsTowardTheRequestLimit(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	carrier := lastUserIndex(o.history)
	keep := turnWindowStart(o.history, imageTurns)
	o.carryImages(keep, maxRequestImageBytes)
	if m := o.history[carrier]; len(m.Images) != 0 || !strings.HasSuffix(m.Content, postedLine(ref, "you")+", no longer attached.") {
		t.Fatalf("a full request carries %d images: %q", len(m.Images), m.Content)
	}
	o.carryImages(keep, 0)
	if m := o.history[carrier]; len(m.Images) != 1 || strings.Count(m.Content, postedLine(ref, "you")) != 1 {
		t.Fatalf("with room the message carries %d images: %q", len(m.Images), m.Content)
	}
}

// Requests that add no user message carry nothing; the next user message
// carries the picture.
func TestPostedPictureWaitsForTheNextUserMessage(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "", StreamOptions{Background: true}); err != nil {
		t.Fatal(err)
	}
	if got := imagesBy(t, fake.sent()[1]); len(got) != 0 {
		t.Fatalf("a background report carried a picture: %v", got)
	}
	if _, err := o.ThinkFull(t.Context(), "What is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if carrier := messageWith(t, fake.sent()[2], "What is it?"); len(carrier.Images) != 1 || !strings.Contains(carrier.Content, postedLine(ref, "you")) {
		t.Fatalf("the next user message carries %d images: %q", len(carrier.Images), carrier.Content)
	}
}

// Once the reply has left the history, its line says the picture is no
// longer attached, and the archive keeps only what the user said.
func TestPostedPictureLineIsNotTheUsersWords(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	carrier := lastUserIndex(o.history)
	o.history, o.working = o.history[carrier:], o.working[carrier:]
	o.carryImages(0, 0)
	if m := o.history[0]; len(m.Images) != 0 || !strings.HasSuffix(m.Content, postedLine(ref, "you")+", no longer attached.") {
		t.Fatalf("with its reply gone the message carries %d images: %q", len(m.Images), m.Content)
	}
	if notes := o.recallNotes(); notes != "" {
		t.Fatalf("a picture line recalled archived notes: %q", notes)
	}
	o.archiveTurns(o.history, o.working)
	entries := o.ContinuityArchive()
	if note := entries[len(entries)-1].Summary; note != "user: What colour is it?\nassistant: It is a grey rectangle.\n" {
		t.Fatalf("archive note = %q", note)
	}
}

// A native-audio persona's requests carry recordings where pictures go. A
// conversation holding a posted picture is refused before any request.
func TestNativeAudioPersonaIsSentNoPostedPicture(t *testing.T) {
	fake := &visionFake{}
	o, _ := postedPicture(t, fake)
	o.SetAudioResolver(noRecordings{})
	if _, err := o.ThinkFull(t.Context(), "Go on.", StreamOptions{}); !errors.Is(err, ErrImageProjection) {
		t.Fatalf("turn: %v", err)
	}
	if len(fake.sent()) != 1 {
		t.Fatal("a request reached the model")
	}
	o.carryImages(0, 0)
	for _, m := range o.history {
		if len(m.Images) > 0 || strings.Contains(m.Content, "a picture you posted") {
			t.Fatalf("a native-audio history carries a posted picture: %q", m.Content)
		}
	}
}
