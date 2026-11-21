package pipeline

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// imageResultWorker finishes with an assistant turn that carries images.
type imageResultWorker struct {
	*slowWorker
	refs []chatmedia.Ref
}

func (w *imageResultWorker) History() []brain.Turn {
	return []brain.Turn{{Role: "assistant", Content: w.reply, Attachments: w.refs}}
}

type imageOrderedBrain struct {
	*orderedBrain
}

func (b *imageOrderedBrain) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	b.record("media: " + text + " [" + strings.Join(ids, ",") + "]")
	return nil
}

func count(lines []string, want string) int {
	n := 0
	for _, line := range lines {
		if line == want {
			n++
		}
	}
	return n
}

// An image result is shown when it finishes, so a follow-up the user sends
// before its speech starts must find it in history. Its speech still plays,
// or is skipped, exactly once without saving it again.
func TestShownImageResultReachesHistoryBeforeItsSpeech(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		name := "spoken after the follow-up"
		if skipped {
			name = "speech dropped after the follow-up"
		}
		t.Run(name, func(t *testing.T) { shownImageResultReachesHistory(t, skipped) })
	}
}

func shownImageResultReachesHistory(t *testing.T, skipped bool) {
	const result = "Here is the diagram."
	const reply = "It shows two services."
	const followUp = "what does the left box mean"
	b := &imageOrderedBrain{&orderedBrain{reply: reply, release: make(chan struct{}), entered: make(chan struct{})}}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Brain: b, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("image-follow-up"); err != nil {
		t.Fatal(err)
	}
	seen := watchResultOrder(p.Events)
	worker := &imageResultWorker{slowWorker: &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: result}, refs: []chatmedia.Ref{{ID: "diagram"}}}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	spawnAttributedJob(t, a, p, "dylan")
	<-worker.started

	// Another turn holds the speaker when the job finishes.
	p.incActiveTurn()
	close(worker.done)
	var finished events.JobFinished
	select {
	case finished = <-seen.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
	if !finished.SpeechPending || len(finished.Attachments) != 1 {
		t.Fatalf("finished = %+v, want a speech-pending image result", finished)
	}

	turnDone := make(chan error, 1)
	go func() { turnDone <- p.RunTurnTextMode(t.Context(), followUp) }()
	<-b.entered
	history := b.Log()
	saved, asked := indexOf(history, "appended: "+result), indexOf(history, "user: "+followUp)
	if saved < 0 || asked < saved || count(history, "media: "+result+" [diagram]") != 1 {
		t.Fatalf("history at the follow-up = %v, want the shown image result before it", history)
	}
	select {
	case e := <-seen.delivered:
		t.Fatalf("speech delivered while another turn held the speaker: %+v", e)
	default:
	}

	if skipped {
		a.discardPendingSpeech()
	}
	p.decActiveTurn()
	close(b.release)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	select {
	case delivered := <-seen.delivered:
		if delivered.Spoken == skipped || delivered.Summary != result || len(delivered.Attachments) != 1 {
			t.Fatalf("delivered = %+v", delivered)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("result speech never resolved: %v", seen.Log())
	}
	a.discardPendingSpeech()
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case e := <-seen.delivered:
		t.Fatalf("result delivered twice: %+v", e)
	default:
	}
	order := seen.Log()
	wantSpoken := 1
	if skipped {
		wantSpoken = 0
	}
	if n := count(order, "speaking: "+result); n != wantSpoken {
		t.Fatalf("result spoken %d times, want %d: %v", n, wantSpoken, order)
	}
	history = b.Log()
	if count(history, "appended: "+result) != 1 || count(history, "media: "+result+" [diagram]") != 1 {
		t.Fatalf("history = %v, want the image result saved once", history)
	}
	if p.BackgroundPending() {
		t.Fatal("committed result still pending")
	}
}

// Results held behind speech are saved in the order the chat shows them:
// image results when they finish, a text-only result when its speech starts.
func TestHeldTextAndImageResultsKeepShownOrder(t *testing.T) {
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	b := &imageOrderedBrain{&orderedBrain{release: make(chan struct{}), entered: make(chan struct{})}}
	p := &Pipeline{Brain: b, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("mixed-held"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var shown, spoken []string
	delivered := make(chan struct{}, 3)
	p.Events.SubscribeAll(func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e := e.(type) {
		case events.JobFinished:
			if e.ShownAtFinish() {
				shown = append(shown, e.Summary)
			}
		case events.JobResultDelivered:
			if len(e.Attachments) == 0 {
				shown = append(shown, e.Summary)
			}
			delivered <- struct{}{}
		case events.SpeakingStarted:
			spoken = append(spoken, e.Text)
		}
	})
	a := NewAgency(p, nil, nil, &config.Config{}, nil)
	p.Agency = a
	p.incActiveTurn()
	for _, r := range []struct {
		id, text string
		refs     []chatmedia.Ref
	}{
		{"a", "First image.", []chatmedia.Ref{{ID: "a"}}},
		{"b", "Text result.", nil},
		{"c", "Second image.", []chatmedia.Ref{{ID: "c"}}},
	} {
		a.emitJobTrace(r.id, a.queueResult(r.id, "task", r.text, r.refs, 0))
	}
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	wantHeld := []string{"appended: First image.", "media: First image. [a]", "appended: Second image.", "media: Second image. [c]"}
	if got := b.Log(); !slices.Equal(got, wantHeld) {
		t.Fatalf("history while held = %v, want only the shown image results", got)
	}
	p.decActiveTurn()
	for range 3 {
		select {
		case <-delivered:
		case <-time.After(3 * time.Second):
			t.Fatal("held speech did not resolve")
		}
	}
	if err := p.commitWorkerReplies(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"First image.", "Text result.", "Second image."}; !slices.Equal(spoken, want) {
		t.Fatalf("spoken = %v, want %v", spoken, want)
	}
	var saved []string
	for _, line := range b.Log() {
		if text, ok := strings.CutPrefix(line, "appended: "); ok {
			saved = append(saved, text)
		}
	}
	if !slices.Equal(saved, shown) || len(saved) != 3 {
		t.Fatalf("history order %v, chat order %v", saved, shown)
	}
}
