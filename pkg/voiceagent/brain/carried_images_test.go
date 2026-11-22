package brain

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
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

// Carried picture states as a request names them.
const (
	attached    = "attached"
	agedOut     = "no longer attached"
	cannotView  = "not shown, because this model cannot view images"
	notAttached = "not attached to this request"
	postedByYou = "a picture you posted"
	openedByYou = "a picture you opened"
)

// pictureLine is the line that names a carried picture in a state.
func pictureLine(ref chatmedia.Ref, state, what string) string {
	return "Picture " + ref.ID + " " + state + ": " + ref.Name + ", " + what + "."
}

// pictureNamed is how every line naming ref begins.
func pictureNamed(ref chatmedia.Ref) string { return "Picture " + ref.ID + " " }

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
			if got := strings.Count(carrier.Content, pictureLine(ref, attached, postedByYou)); got != 1 {
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
			lines += strings.Count(m.Content, pictureNamed(ref))
		}
		if wantImages := map[bool]int{true: 1, false: 0}[i < imageTurns]; images != wantImages || lines != 1 {
			t.Fatalf("request for turn-%d carries %d images and names the picture %d times", i+2, images, lines)
		}
	}
	aged := messageWith(t, sent[len(sent)-1], "turn-2")
	if !strings.HasSuffix(aged.Content, pictureLine(ref, agedOut, postedByYou)) {
		t.Fatalf("the aged message reads %q", aged.Content)
	}
	assertWorkingAligned(t, o)

	assertAgedOnLoad(t, o, "turn-2", pictureLine(ref, agedOut, postedByYou))
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
		named := false
		for _, m := range r.buildMessages("") {
			if len(m.Images) > 0 {
				t.Fatalf("%s: a message carries %d images", name, len(m.Images))
			}
			if strings.Contains(m.Content, carrier) {
				named = true
				if strings.Count(m.Content, line) != 1 {
					t.Fatalf("%s: the aged message reads %q", name, m.Content)
				}
			}
		}
		if !named {
			t.Fatalf("%s: no message says %q", name, carrier)
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
				if n := strings.Count(m.Content, pictureNamed(ref)); n != 1 {
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
	if len(carrier.Images) != 1 || !strings.Contains(carrier.Content, pictureLine(ref, attached, "a picture Venessa posted")) {
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
					lines += strings.Count(m.Content, pictureLine(ref, cannotView, postedByYou))
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
			if len(carrier.Images) != 1 || !strings.Contains(carrier.Content, pictureLine(gone, agedOut, postedByYou)) || !strings.Contains(carrier.Content, pictureLine(ref, attached, postedByYou)) {
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
	o.maxImageBytes = modelImageSize(t, ref) - 1
	full := o.buildMessages("")
	if m := messageWith(t, api.ChatRequest{Messages: full}, "What colour is it?"); len(m.Images) != 0 || !strings.HasSuffix(m.Content, pictureLine(ref, agedOut, postedByYou)) {
		t.Fatalf("a full request carries %d images: %q", len(m.Images), m.Content)
	}
	o.maxImageBytes = 0
	if m := messageWith(t, built(o), "What colour is it?"); len(m.Images) != 1 || strings.Count(m.Content, pictureNamed(ref)) != 1 {
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
	if carrier := messageWith(t, fake.sent()[2], "What is it?"); len(carrier.Images) != 1 || !strings.Contains(carrier.Content, pictureLine(ref, attached, postedByYou)) {
		t.Fatalf("the next user message carries %d images: %q", len(carrier.Images), carrier.Content)
	}
}

// Once the reply has left the history, the message carries neither its
// picture nor a line for it, and the archive keeps only what the user said.
func TestPostedPictureLineIsNotTheUsersWords(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if m := messageWith(t, fake.sent()[1], "What colour is it?"); len(m.Images) != 1 {
		t.Fatalf("with its reply the message carries %d images: %q", len(m.Images), m.Content)
	}
	carrier := lastUserIndex(o.history)
	o.history, o.working = o.history[carrier:], o.working[carrier:]
	if m := messageWith(t, built(o), "What colour is it?"); len(m.Images) != 0 || strings.Contains(m.Content, pictureNamed(ref)) {
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

// assertUsersWords checks that the user turn that said input reads exactly
// input in the projection, the archive and the recall input.
func assertUsersWords(t *testing.T, o *OllamaBrain, input string) {
	t.Helper()
	j := -1
	for i, turn := range o.working {
		if turn.Role == "user" && turn.Content == input {
			j = i
		}
	}
	if j < 0 {
		t.Fatalf("no user turn says %q", input)
	}
	if got := o.history[j].Content; got != input {
		t.Fatalf("the prompt history holds %q", got)
	}
	if got := o.ContextHistory()[j].Content; got != input {
		t.Fatalf("the projected user turn reads %q", got)
	}
	if got := o.archiveLine(o.history[j], &o.working[j]); got != "user: "+input+"\n" {
		t.Fatalf("the archive line reads %q", got)
	}
	_, said := decodeOllamaUser(o.history[j].Content)
	if got := withoutImageNotes(said, o.working[j].Attachments); got != input {
		t.Fatalf("the recall input reads %q", got)
	}
}

// A line the user types in the form of a picture line, even naming a picture
// the request carries, stays the user's own text, as written, everywhere.
func TestUserTextInThePictureLineFormIsTheUsers(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	input := "look at this\nPicture " + ref.ID + " attached: x.png, a picture you posted."
	if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	carrier := messageWith(t, fake.sent()[1], "look at this")
	if !strings.HasPrefix(carrier.Content, input+"\n"+pictureLine(ref, attached, postedByYou)) || strings.Count(carrier.Content, input) != 1 || strings.Count(carrier.Content, pictureNamed(ref)) != 2 || len(carrier.Images) != 1 {
		t.Fatalf("the message carries %d images and reads %q", len(carrier.Images), carrier.Content)
	}
	assertUsersWords(t, o, input)
	for _, in := range []string{"t3", "t4", "t5", "t6"} {
		if _, err := o.ThinkFull(t.Context(), in, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	sent := fake.sent()
	for i, req := range sent[1:] {
		if m := messageWith(t, req, "look at this"); !strings.HasPrefix(m.Content, input+"\n"+pictureNamed(ref)) || strings.Count(m.Content, input) != 1 {
			t.Fatalf("request %d reads %q", i+1, m.Content)
		}
	}
	if aged := messageWith(t, sent[len(sent)-1], "look at this"); aged.Content != input+"\n"+pictureLine(ref, agedOut, postedByYou) || len(aged.Images) != 0 {
		t.Fatalf("the aged message carries %d images and reads %q", len(aged.Images), aged.Content)
	}
	assertUsersWords(t, o, input)
}

// With no picture anywhere, a typed line in the picture line form is sent,
// projected and archived as written on every request.
func TestUserTextInThePictureLineFormWithNoPicture(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	input := "notes\nPicture " + strings.Repeat("0f", 32) + " attached: y.png, a picture you posted."
	for _, in := range []string{input, "t2", "t3", "t4", "t5"} {
		if _, err := o.ThinkFull(t.Context(), in, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
		assertUsersWords(t, o, input)
	}
	for i, req := range fake.sent() {
		m := messageWith(t, req, "notes\nPicture ")
		if !strings.HasPrefix(m.Content, input) || strings.Count(m.Content, "\nPicture ") != 1 || len(imagesBy(t, req)) != 0 {
			t.Fatalf("request %d reads %q", i, m.Content)
		}
		if i > 0 && m.Content != input {
			t.Fatalf("request %d reads %q", i, m.Content)
		}
	}
}

// A native-audio persona's requests carry recordings where pictures go, so
// a posted picture rides on no message of its history.
func TestNativeAudioPersonaIsSentNoPostedPicture(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	ref := photos(t, 1, 32, 32)[0]
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Echo.")
	o.SetAudioResolver(noRecordings{})
	o.LoadHistory([]Turn{
		{Role: "user", Content: "Draw me a chart."},
		{Role: "assistant", Content: "Here is the chart.", Attachments: []chatmedia.Ref{ref}},
		{Role: "user", Content: "What colour is it?"},
		{Role: "assistant", Content: "Grey."},
	})
	if err := o.prepareImageHistory(t.Context(), StreamOptions{Background: true}); err != nil {
		t.Fatal(err)
	}
	for _, m := range o.buildMessages("") {
		if len(m.Images) > 0 || strings.Contains(m.Content, pictureNamed(ref)) {
			t.Fatalf("a native-audio request carries a posted picture: %q", m.Content)
		}
	}
	if _, err := o.ThinkFull(t.Context(), "Go on.", StreamOptions{}); !errors.Is(err, ErrImageProjection) {
		t.Fatalf("turn: %v", err)
	}
	if len(fake.sent()) != 0 {
		t.Fatal("a request reached the model")
	}
}

func modelImageSize(t *testing.T, ref chatmedia.Ref) int {
	t.Helper()
	data, err := chatmedia.DefaultStore().ModelImage(ref)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

// File names that look like a picture line, the user's or a posted one, do
// not change how many times either is named.
func TestPictureNamesDoNotConfuseTheirLines(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 2, 32, 32)
	mine, posted := refs[0], refs[1]
	mine.Name = "x): a picture y.png"
	posted.Name = "Picture 1 attached: z): a picture, no longer attached.png"
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	if _, err := o.ThinkFull(t.Context(), "Draw it.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReplyMedia("Here.\nz", []chatmedia.Ref{posted}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ThinkFull(t.Context(), "look at mine", StreamOptions{Attachments: []chatmedia.Ref{mine}}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"t3", "t4", "t5", "t6", "t7"} {
		if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
		assertWorkingAligned(t, o)
	}
	for i, req := range fake.sent()[1:] {
		m := messageWith(t, req, "look at mine")
		if n := strings.Count(m.Content, "(x): a picture y.png)"); n > 1 {
			t.Fatalf("request %d names the user's picture %d times: %q", i, n, m.Content)
		}
		if n := strings.Count(m.Content, pictureNamed(posted)); n != 1 {
			t.Fatalf("request %d names the posted picture %d times: %q", i, n, m.Content)
		}
	}
	last := messageWith(t, fake.sent()[len(fake.sent())-1], "look at mine")
	if !strings.Contains(last.Content, "(x): a picture y.png): no longer attached.") || !strings.Contains(last.Content, pictureLine(posted, agedOut, postedByYou)) {
		t.Fatalf("the aged message reads %q", last.Content)
	}
	if got := o.ContextHistory()[2].Content; strings.Contains(got, pictureNamed(posted)) {
		t.Fatalf("the projected user turn reads %q", got)
	}
}

// A picture a model was never shown because it cannot view images is not
// later said to be "no longer attached".
func TestNeverShownPictureKeepsSayingSoWhenItAges(t *testing.T) {
	fake := &visionFake{textOnly: true}
	o, ref := postedPicture(t, fake)
	for _, input := range []string{"What colour is it?", "t3", "t4", "t5"} {
		if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	m := messageWith(t, fake.sent()[len(fake.sent())-1], "What colour is it?")
	if !strings.Contains(m.Content, pictureLine(ref, cannotView, postedByYou)) || strings.Count(m.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("the aged message reads %q", m.Content)
	}
}

// When the server cannot say whether the model views images, a request that
// holds only posted pictures leaves them out without claiming the model
// cannot view them, and the next request shows them.
func TestPostedPictureWaitsOutAFailedCapabilityCheck(t *testing.T) {
	fake := &visionFake{}
	o, ref := postedPicture(t, fake)
	var told []chatmedia.Ref
	opts := StreamOptions{OnImagesWithheld: func(_ string, refs []chatmedia.Ref) { told = append(told, refs...) }}
	fake.showFails.Store(true)
	if _, err := o.ThinkFull(t.Context(), "What colour is it?", opts); err != nil {
		t.Fatal(err)
	}
	req := fake.sent()[1]
	carrier := messageWith(t, req, "What colour is it?")
	if len(imagesBy(t, req)) != 0 || !strings.Contains(carrier.Content, pictureLine(ref, notAttached, postedByYou)) || strings.Contains(carrier.Content, "cannot view") {
		t.Fatalf("with no capability answer the message reads %q", carrier.Content)
	}
	fake.showFails.Store(false)
	if _, err := o.ThinkFull(t.Context(), "And now?", opts); err != nil {
		t.Fatal(err)
	}
	carrier = messageWith(t, fake.sent()[2], "What colour is it?")
	if len(carrier.Images) != 1 || !strings.Contains(carrier.Content, pictureLine(ref, attached, postedByYou)) || strings.Count(carrier.Content, pictureNamed(ref)) != 1 {
		t.Fatalf("the next request's message carries %d images: %q", len(carrier.Images), carrier.Content)
	}
	if len(told) != 0 {
		t.Fatalf("the user was told about pictures they did not attach: %v", told)
	}
	assertWorkingAligned(t, o)
}

// A group discussion records its message, pictures included, before the
// request that also names them; they count once against the request limit.
func TestGroupMessagePicturesAreCountedOnce(t *testing.T) {
	fake := &visionFake{}
	o, posted := postedPicture(t, fake)
	own := photos(t, 2, 32, 32)[1]
	o.maxImageBytes = modelImageSize(t, own) + modelImageSize(t, posted)
	snap := o.ContinuitySnapshot()
	message := Turn{Role: "user", Content: "What do you all think?", Attachments: []chatmedia.Ref{own}}
	snap.Canonical = append(snap.Canonical, message)
	snap.Working = append(snap.Working, message)
	if err := o.ImportContinuity(snap); err != nil {
		t.Fatal(err)
	}
	if err := o.prepareImageHistory(t.Context(), StreamOptions{Discussion: &discussion.Directive{}, Attachments: []chatmedia.Ref{own}}); err != nil {
		t.Fatal(err)
	}
	if m := messageWith(t, built(o), "What do you all think?"); len(m.Images) != 2 || !strings.Contains(m.Content, pictureLine(posted, attached, postedByYou)) {
		t.Fatalf("the group message carries %d images: %q", len(m.Images), m.Content)
	}
}

// Posted pictures on several messages share one request's limit with the
// user's own: the user's count first, then the newest posted pictures.
func TestPostedPicturesShareTheRequestLimit(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	refs := photos(t, 4, 32, 32)
	fake := &visionFake{}
	o := fake.brain(t, budgetConfig(), "You are Obey.")
	o.maxImageBytes = 3 * modelImageSize(t, refs[0])
	for i, input := range []string{"draw a", "draw b", "draw c"} {
		if _, err := o.ThinkFull(t.Context(), input, StreamOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := o.SetReplyMedia("Here.\npicture", refs[i:i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.ThinkFull(t.Context(), "compare with mine", StreamOptions{Attachments: refs[3:]}); err != nil {
		t.Fatal(err)
	}
	req := fake.sent()[len(fake.sent())-1]
	for text, want := range map[string]int{"compare with mine": 2, "draw c": 1, "draw b": 0} {
		if m := messageWith(t, req, text); len(m.Images) != want {
			t.Fatalf("%q carries %d images, want %d: %q", text, len(m.Images), want, m.Content)
		}
	}
	if m := messageWith(t, req, "draw b"); !strings.Contains(m.Content, pictureLine(refs[0], agedOut, postedByYou)) {
		t.Fatalf("the picture left out reads %q", m.Content)
	}
	assertWorkingAligned(t, o)
}
