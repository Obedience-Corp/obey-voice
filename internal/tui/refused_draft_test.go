package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

// toEcho names a persona that cannot take a picture. Its draft is checked
// against the persona selected when it is sent, so it is refused only after
// it has left the input.
const toEcho = "Echo, what is on my screen?"

var (
	pressEnter     = tea.KeyMsg{Type: tea.KeyEnter}
	pressBackspace = tea.KeyMsg{Type: tea.KeyBackspace}
	pressF4        = tea.KeyMsg{Type: tea.KeyF4}
)

// attachNew puts a picture in the draft that differs from every other one
// attached here, so that drafts can be told apart.
func (c *textOnlyChat) attachNew(t *testing.T) chatmedia.Ref {
	t.Helper()
	c.shade += 40
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = c.shade
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	cmd := c.model.importMedia(nil, data.Bytes())
	if cmd == nil {
		t.Fatal("missing import")
	}
	c.deliver(cmd())
	return c.model.media.draft[len(c.model.media.draft)-1]
}

// deliver passes a message to the conversation as the program does.
func (c *textOnlyChat) deliver(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	c.model, cmd = c.model.Update(msg)
	return cmd
}

// typeText types into the input.
func (c *textOnlyChat) typeText(text string) {
	c.deliver(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

// produced runs cmd and returns the message of type T it led to.
func produced[T any](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	for _, msg := range flattenCmd(cmd) {
		if v, ok := msg.(T); ok {
			return v
		}
	}
	var none T
	t.Fatalf("the command produced no %T", none)
	return none
}

// routeByName sends a message that names Echo to a persona that takes native
// audio, as routing by name does before the turn runs, and any other message
// to the text-only persona.
func (c *textOnlyChat) routeByName(t *testing.T) *brain.OllamaBrain {
	t.Helper()
	echo, err := brain.NewNativeOllama(t.Context(), c.cfg, c.client, noRecordings{})
	if err != nil {
		t.Fatal(err)
	}
	c.pipeline.AdmitInput = func(_ context.Context, input string, _ bool) (pipeline.ResponseAdmission, error) {
		c.pipeline.Brain = c.brain
		if strings.HasPrefix(input, "Echo,") {
			c.pipeline.Brain = echo
		}
		return pipeline.ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "p1", DisplayName: "Persona"}}, nil
	}
	return echo
}

// refusedAfterSending sends text with a picture and returns the picture and
// the refusal of its turn, not yet delivered to the conversation.
func (c *textOnlyChat) refusedAfterSending(t *testing.T, text string) (chatmedia.Ref, textTurnDoneMsg) {
	t.Helper()
	// The draft is checked against the text-only persona, which keeps a
	// picture for the conversation.
	c.pipeline.Brain = c.brain
	ref := c.attachNew(t)
	c.typeText(text)
	checked := produced[mediaValidated](t, c.deliver(pressEnter))
	refusal := produced[textTurnDoneMsg](t, c.deliver(checked))
	if !pipeline.ImagesRefused(refusal.err) {
		t.Fatalf("the turn was not refused over its picture: %v", refusal.err)
	}
	return ref, refusal
}

// reports collects what the conversation is told from here on.
func (c *textOnlyChat) reports() *[]string {
	var said []string
	c.bus.SubscribeAll(func(e events.Event) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch e := e.(type) {
		case events.Error:
			said = append(said, "error: "+e.Message)
		case events.Info:
			said = append(said, "info: "+e.Message)
		}
	})
	return &said
}

// holds reports the input holding exactly this text and these pictures.
func (c *textOnlyChat) holds(text string, refs ...chatmedia.Ref) bool {
	return c.model.input.Value() == text && c.model.editor.text() == text && reflect.DeepEqual(refIDs(c.model.media.draft), refIDs(refs))
}

func (c *textOnlyChat) input() string {
	return "input=" + c.model.input.Value() + " editor=" + c.model.editor.text() + " pictures=" + strings.Join(refIDs(c.model.media.draft), ",") + " reason=" + c.model.media.err
}

func refIDs(refs []chatmedia.Ref) []string {
	var ids []string
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return ids
}

// The message names a persona that cannot take a picture. Its text and
// picture come back to an empty input at once, and the conversation says why
// the message it shows was not answered.
func TestPictureRefusedAfterRoutingComesBackToTheDraft(t *testing.T) {
	c := newTextOnlyChat(t)
	echo := c.routeByName(t)
	said := c.reports()
	ref, refusal := c.refusedAfterSending(t, toEcho)
	if !c.holds("") {
		t.Fatalf("the sent draft stayed in the input: %s", c.input())
	}
	c.deliver(refusal)
	if !c.holds(toEcho, ref) || !strings.Contains(c.model.media.err, "native audio") {
		t.Fatalf("the draft did not come back: %s", c.input())
	}
	if len(*said) != 1 || !strings.HasPrefix((*said)[0], "error: ") || !strings.Contains((*said)[0], "native audio") {
		t.Fatalf("the conversation was told: %q", *said)
	}
	if len(c.requests()) != 0 || len(c.brain.History()) != 0 || len(echo.History()) != 0 {
		t.Fatal("a refused picture reached a model or the record")
	}
}

// The refusal lands after the user has started the next message. That message
// is left as typed and sent on its own. The refused one waits and returns
// when the input is empty.
func TestRefusedPictureWaitsWhileTheNextMessageIsTyped(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	said := c.reports()
	ref, refusal := c.refusedAfterSending(t, toEcho)

	c.typeText("and also")
	c.deliver(refusal)
	if !c.holds("and also") || c.model.media.err != "" {
		t.Fatalf("the next message was changed: %s", c.input())
	}
	want := []string{
		"error: this persona takes native audio and cannot be sent pictures; select a persona that views images",
		"info: That message was not sent. It returns to the input with its picture once the input is empty.",
	}
	if !reflect.DeepEqual(*said, want) {
		t.Fatalf("the conversation was told: %q", *said)
	}

	turn := c.deliver(pressEnter)
	if !c.holds(toEcho, ref) || !strings.Contains(c.model.media.err, "native audio") {
		t.Fatalf("the refused message did not return: %s", c.input())
	}
	if done := produced[textTurnDoneMsg](t, turn); done.err != nil || done.input != "and also" || len(done.refs) != 0 {
		t.Fatalf("the next message: %+v", done)
	}
	history := c.brain.History()
	if len(history) != 2 || history[0].Content != "and also" || len(history[0].Attachments) != 0 {
		t.Fatalf("the conversation record: %+v", history)
	}

	c.typeText(" Thanks.")
	if !c.holds(toEcho+" Thanks.", ref) {
		t.Fatalf("typing did not go on after the returned text: %s", c.input())
	}
}

// The user has attached another picture and typed when the refusal lands. The
// two drafts are never joined: the refused one returns only once both the
// new picture and the new text are gone.
func TestRefusedPictureIsNotJoinedToANewDraft(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	next := c.attachNew(t)
	c.typeText("ok")
	c.deliver(refusal)
	if !c.holds("ok", next) {
		t.Fatalf("the new draft was changed: %s", c.input())
	}
	c.deliver(pressF4)
	if !c.holds("ok") {
		t.Fatalf("after the new picture was removed: %s", c.input())
	}
	c.deliver(pressBackspace)
	if !c.holds("o") {
		t.Fatalf("the refused message returned to an input in use: %s", c.input())
	}
	c.deliver(pressBackspace)
	if !c.holds(toEcho, ref) {
		t.Fatalf("the refused message did not return to the empty input: %s", c.input())
	}
}

// The next draft is being checked when the refusal lands. It is sent as it
// was checked, and the refused one returns after it.
func TestRefusedPictureWaitsForADraftBeingChecked(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	c.pipeline.Brain = c.brain
	next := c.attachNew(t)
	c.typeText("And this one?")
	check := c.deliver(pressEnter)
	if !c.model.media.validating {
		t.Fatal("the next draft is not being checked")
	}
	c.deliver(refusal)
	if !c.holds("And this one?", next) || !c.model.media.validating {
		t.Fatalf("a draft being checked was changed: %s", c.input())
	}

	turn := c.deliver(produced[mediaValidated](t, check))
	if !c.holds(toEcho, ref) {
		t.Fatalf("the refused message did not return: %s", c.input())
	}
	done := produced[textTurnDoneMsg](t, turn)
	if done.err != nil || done.input != "And this one?" || !reflect.DeepEqual(done.refs, []chatmedia.Ref{next}) {
		t.Fatalf("the checked draft was not sent as checked: %+v", done)
	}
}

// A typed file path is being imported when the refusal lands. The import
// still clears the path it was typed as, and the refused message waits behind
// the picture it brought in.
func TestRefusedPictureWaitsForAPictureBeingImported(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	path := filepath.Join(t.TempDir(), "next.png")
	if err := os.WriteFile(path, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	typed := `"` + path + `"`
	c.typeText(typed)
	importing := c.deliver(pressEnter)
	if !c.model.media.importing {
		t.Fatal("the typed path is not being imported")
	}
	c.deliver(refusal)
	if !c.holds(typed) {
		t.Fatalf("an input being imported into was changed: %s", c.input())
	}

	c.deliver(produced[mediaImported](t, importing))
	if c.model.input.Value() != "" || len(c.model.media.draft) != 1 || c.model.media.draft[0] == ref {
		t.Fatalf("the imported picture did not replace its path: %s", c.input())
	}
	c.deliver(pressF4)
	if !c.holds(toEcho, ref) {
		t.Fatalf("the refused message did not return: %s", c.input())
	}
}

// A pasted picture is still being imported into an empty input when the
// refusal lands. The refused draft waits, so the pasted picture does not land
// in it.
func TestRefusedPictureIsNotJoinedToAPictureBeingPasted(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	pasting := c.deliver(clipboardPasteMsg{image: testmedia.GIF()})
	if !c.model.media.importing {
		t.Fatal("the pasted picture is not being imported")
	}
	c.deliver(refusal)
	if !c.holds("") {
		t.Fatalf("the refused message returned under a paste: %s", c.input())
	}
	c.deliver(produced[mediaImported](t, pasting))
	if c.model.input.Value() != "" || len(c.model.media.draft) != 1 || c.model.media.draft[0] == ref {
		t.Fatalf("the pasted picture was joined to the refused draft: %s", c.input())
	}
	c.deliver(pressF4)
	if !c.holds(toEcho, ref) {
		t.Fatalf("the refused message did not return: %s", c.input())
	}
}

// The user sends the next message before the refusal is delivered, which
// interrupts the turn in flight. That message goes out and the refused one
// still returns.
func TestRefusedPictureReturnsAfterAMessageSentMeanwhile(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	c.typeText("and also")
	c.deliver(pressEnter)
	if c.model.turnState != turnVoiceCanceling || !c.holds("") {
		t.Fatalf("the next message did not interrupt the turn: state=%v %s", c.model.turnState, c.input())
	}
	turn := c.deliver(refusal)
	if !c.holds(toEcho, ref) || !strings.Contains(c.model.media.err, "native audio") {
		t.Fatalf("the refused message did not return: %s", c.input())
	}
	if done := produced[textTurnDoneMsg](t, turn); done.err != nil || done.input != "and also" || len(done.refs) != 0 {
		t.Fatalf("the next message: %+v", done)
	}
	for _, req := range c.requests() {
		for _, m := range req.Messages {
			if len(m.Images) > 0 {
				t.Fatal("the next message carried the refused picture")
			}
		}
	}
}

// A second refused message waits behind the first while the first is in the
// input.
func TestRefusedPicturesReturnOneAtATime(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	first, refusal := c.refusedAfterSending(t, toEcho)
	c.deliver(refusal)
	if !c.holds(toEcho, first) {
		t.Fatalf("the first refused message did not return: %s", c.input())
	}
	// The user gives up on that draft and sends another to the same persona.
	c.deliver(pressF4)
	for range toEcho {
		c.deliver(pressBackspace)
	}
	said := c.reports()
	second, refusal := c.refusedAfterSending(t, "Echo, and this one?")
	// Meanwhile the first draft is brought back by hand.
	again := c.attachNew(t)
	c.deliver(refusal)
	if !c.holds("", again) || len(*said) != 2 {
		t.Fatalf("the second refused message did not wait: %s, told %q", c.input(), *said)
	}
	c.deliver(pressF4)
	if !c.holds("Echo, and this one?", second) {
		t.Fatalf("the second refused message did not return: %s", c.input())
	}
}

// A message with a picture is parked, waiting for the turn it interrupted to
// end. The refused draft stays out of the input until that message has gone,
// so the two sets of pictures cannot end up in one draft.
func TestRefusedPictureWaitsBehindAMessageParkedForSending(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)

	parked := c.attachNew(t)
	c.model.media.pending, c.model.media.draft = c.model.media.draft, nil
	c.deliver(refusal)
	if !c.holds("") {
		t.Fatalf("the refused message returned beside a parked one: %s", c.input())
	}
	c.model.dispatchTextTurn("And this one?")
	c.deliver(pressF4)
	if !c.holds(toEcho, ref) || parked == ref {
		t.Fatalf("the refused message did not return on its own: %s", c.input())
	}
}

// Clearing the conversation drops its drafts. A refusal that arrives
// afterwards belongs to the cleared conversation and returns nothing.
func TestRefusalAfterTheConversationIsClearedReturnsNothing(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	_, refusal := c.refusedAfterSending(t, toEcho)
	c.model.clearTranscript()
	c.deliver(refusal)
	if !c.holds("") || len(c.model.media.refused) != 0 {
		t.Fatalf("a cleared conversation was given a draft: %s", c.input())
	}
}

// A turn canceled while its pictures are being checked was interrupted, not
// refused. Its draft does not come back, as for any interrupted message.
func TestPictureTurnCanceledDuringItsCheckIsNotRefused(t *testing.T) {
	c := newTextOnlyChat(t)
	c.attach(t)
	c.typeText("What is on my screen?")
	turn := c.deliver(produced[mediaValidated](t, c.deliver(pressEnter)))
	if turn == nil || c.model.turnCancel == nil {
		t.Fatal("the draft was not sent")
	}
	c.showHangs.Store(true)
	done := make(chan tea.Msg, 1)
	go func() { done <- turn() }()
	<-c.asked
	c.model.turnCancel()
	msg, ok := (<-done).(textTurnDoneMsg)
	if !ok || msg.err == nil || pipeline.ImagesRefused(msg.err) {
		t.Fatalf("the canceled turn ended with: %+v", msg)
	}
	c.deliver(msg)
	if !c.holds("") || len(c.model.media.refused) != 0 {
		t.Fatalf("an interrupted message was given back: %s", c.input())
	}
	if len(c.requests()) != 0 || len(c.brain.History()) != 0 {
		t.Fatal("a canceled turn reached the model or the record")
	}
}

// The desktop front end drives the conversation directly, past its Update. A
// held draft still returns as soon as one of its actions empties the input.
func TestHeldPictureReturnsAfterADesktopAction(t *testing.T) {
	c := newTextOnlyChat(t)
	c.routeByName(t)
	ref, refusal := c.refusedAfterSending(t, toEcho)
	next := c.attachNew(t)
	c.deliver(refusal)
	if !c.holds("", next) {
		t.Fatalf("the new picture was joined to the refused draft: %s", c.input())
	}

	app := newApp(c.cfg, nil, nil)
	app.conversation = c.model
	model, _ := desktopModel{app: app, rt: fakeRuntime()}.Update(DesktopAction{Kind: "attachment_remove", Text: next.ID})
	state := model.(desktopModel).state()
	if len(state.Attachments) != 1 || state.Attachments[0].Ref != ref || !strings.Contains(state.AttachmentError, "native audio") {
		t.Fatalf("the desktop draft: %+v, reason %q", state.Attachments, state.AttachmentError)
	}
}
