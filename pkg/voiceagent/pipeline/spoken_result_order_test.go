package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// orderedBrain keeps one log of everything that enters history: its own
// turns, and replies the host appends. Its stream holds until released.
type orderedBrain struct {
	reply   string
	release chan struct{}
	entered chan struct{}
	once    sync.Once

	mu  sync.Mutex
	log []string
}

func (b *orderedBrain) record(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log = append(b.log, line)
}

func (b *orderedBrain) Log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.log...)
}

func (b *orderedBrain) ThinkStream(ctx context.Context, input string, _ brain.StreamOptions) (*brain.Stream, error) {
	b.record("user: " + input)
	b.once.Do(func() { close(b.entered) })
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		select {
		case <-b.release:
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		out <- b.reply
		b.record("assistant: " + b.reply)
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func (b *orderedBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (b *orderedBrain) AppendAssistantResponse(text string) error {
	return b.AppendAssistantResponseWithIdentity(text, brain.TurnIdentity{})
}
func (b *orderedBrain) AppendAssistantResponseWithIdentity(text string, _ brain.TurnIdentity) error {
	b.record("appended: " + text)
	return nil
}
func (b *orderedBrain) ClearHistory()            {}
func (b *orderedBrain) History() []brain.Turn    { return nil }
func (b *orderedBrain) LoadHistory([]brain.Turn) {}

type resultOrderEvents struct {
	mu  sync.Mutex
	log []string

	finished  chan events.JobFinished
	delivered chan events.JobResultDelivered
}

func watchResultOrder(bus *events.Bus) *resultOrderEvents {
	w := &resultOrderEvents{finished: make(chan events.JobFinished, 4), delivered: make(chan events.JobResultDelivered, 4)}
	bus.SubscribeAll(func(e events.Event) {
		var line string
		switch e := e.(type) {
		case events.JobFinished:
			line = "finished"
			w.finished <- e
		case events.JobResultDelivered:
			line = "delivered: " + e.Summary
			w.delivered <- e
		case events.SpeakingStarted:
			line = "speaking: " + e.Text
		case events.ResponseReady:
			line = "ready: " + e.Response
		default:
			return
		}
		w.mu.Lock()
		w.log = append(w.log, line)
		w.mu.Unlock()
	})
	return w
}

func (w *resultOrderEvents) Log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.log...)
}

func indexOf(lines []string, want string) int {
	for i, line := range lines {
		if line == want {
			return i
		}
	}
	return -1
}

func spawnAttributedJob(t *testing.T, a *Agency, p *Pipeline, persona string) {
	t.Helper()
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{PersonaID: persona, DisplayName: persona, ResponseID: "spawn-" + persona}
	a.HandleRoute(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "look it up", Source: "test"})
}

// A result that finishes while another reply is in flight is shown and saved
// after that reply, at the point the user starts hearing it.
func TestSpokenJobResultFollowsTheReplyItWaitedBehind(t *testing.T) {
	const result = "The fixture has three files."
	const reply = "Here is the other answer."
	b := &orderedBrain{reply: reply, release: make(chan struct{}), entered: make(chan struct{})}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Brain: b, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("spoken-order"); err != nil {
		t.Fatal(err)
	}
	seen := watchResultOrder(p.Events)
	worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: result}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	spawnAttributedJob(t, a, p, "dylan")
	<-worker.started

	turnDone := make(chan error, 1)
	go func() { turnDone <- p.RunTurnTextMode(t.Context(), "question for raw") }()
	<-b.entered
	close(worker.done)
	var finished events.JobFinished
	select {
	case finished = <-seen.finished:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
	if !finished.SpeechPending || finished.Summary != result {
		t.Fatalf("finished = %+v, want a speech-pending result", finished)
	}
	select {
	case e := <-seen.delivered:
		t.Fatalf("result delivered while another reply held the turn: %+v", e)
	default:
	}
	if p.hasWorkerReplies() || p.BackgroundPending() {
		t.Fatal("history could take a result the user has not been given")
	}

	close(b.release)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	var delivered events.JobResultDelivered
	select {
	case delivered = <-seen.delivered:
	case <-time.After(3 * time.Second):
		t.Fatalf("result never delivered: %v", seen.Log())
	}
	if !delivered.Spoken || delivered.Summary != result || delivered.Envelope == nil || delivered.Envelope.DisplayName != "dylan" || delivered.Envelope.ResponseID != finished.Envelope.ResponseID {
		t.Fatalf("delivered = %+v", delivered)
	}
	order := seen.Log()
	ready, shown, spoken := indexOf(order, "ready: "+reply), indexOf(order, "delivered: "+result), indexOf(order, "speaking: "+result)
	if ready < 0 || shown < 0 || spoken < 0 || ready > shown || shown > spoken {
		t.Fatalf("event order = %v, want the reply, then the result shown as its speech starts", order)
	}

	if !p.BackgroundPending() {
		t.Fatal("delivered result is not offered to history")
	}
	if err := p.RunBackgroundTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	history := b.Log()
	answered, saved := indexOf(history, "assistant: "+reply), indexOf(history, "appended: "+result)
	if answered < 0 || saved < 0 || saved < answered {
		t.Fatalf("history = %v, want the result after the reply it was heard after", history)
	}
	if p.BackgroundPending() {
		t.Fatal("committed result still pending")
	}
}

// A result that cannot be spoken is shown and offered to history at once.
func TestUnspokenJobResultIsShownWithItsFinish(t *testing.T) {
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	b := &appendingBrain{}
	p := &Pipeline{Brain: b, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus()}
	p.SetOutputMuted(true)
	if err := p.BeginConversationIdentity("muted-order"); err != nil {
		t.Fatal(err)
	}
	seen := watchResultOrder(p.Events)
	worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "Muted result."}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	spawnAttributedJob(t, a, p, "dylan")
	<-worker.started
	close(worker.done)
	select {
	case finished := <-seen.finished:
		if finished.SpeechPending {
			t.Fatalf("muted result deferred: %+v", finished)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
	if !p.hasWorkerReplies() {
		t.Fatal("unspoken result held from history")
	}
	time.Sleep(20 * time.Millisecond)
	select {
	case e := <-seen.delivered:
		t.Fatalf("unspoken result delivered twice: %+v", e)
	default:
	}
}

// A held result whose speech is skipped, or dropped from the queue, is still
// delivered exactly once and still reaches history.
func TestHeldJobResultIsDeliveredWhenItsSpeechIsSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		skip func(*Pipeline, *Agency)
	}{
		{name: "muted before it played", skip: func(p *Pipeline, a *Agency) {
			p.SetOutputMuted(true)
			p.decActiveTurn()
		}},
		{name: "dropped from the queue", skip: func(_ *Pipeline, a *Agency) { a.discardPendingSpeech() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			b := &appendingBrain{}
			p := &Pipeline{Brain: b, TTS: &progressiveFakeTTS{}, Player: player, Events: events.NewBus()}
			if err := p.BeginConversationIdentity("skipped-order"); err != nil {
				t.Fatal(err)
			}
			seen := watchResultOrder(p.Events)
			worker := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "Skipped result."}
			jobs := job.NewTable(t.Context())
			t.Cleanup(jobs.Close)
			a := NewAgency(p, jobs, worker, &config.Config{}, nil)
			a.Wire()
			spawnAttributedJob(t, a, p, "dylan")
			<-worker.started
			p.incActiveTurn()
			close(worker.done)
			select {
			case finished := <-seen.finished:
				if !finished.SpeechPending {
					t.Fatalf("result not held for speech: %+v", finished)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("job did not finish")
			}
			tc.skip(p, a)
			select {
			case delivered := <-seen.delivered:
				if delivered.Spoken || delivered.Summary != "Skipped result." {
					t.Fatalf("delivered = %+v", delivered)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("skipped result was lost")
			}
			a.discardPendingSpeech()
			select {
			case e := <-seen.delivered:
				t.Fatalf("result delivered twice: %+v", e)
			default:
			}
			if err := p.commitWorkerReplies(); err != nil {
				t.Fatal(err)
			}
			if len(b.appended) != 1 || b.appended[0] != "Skipped result." {
				t.Fatalf("history = %v", b.appended)
			}
		})
	}
}
