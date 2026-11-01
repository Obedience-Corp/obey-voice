package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type captionFixtureBrain struct {
	nativeInputBrain
	mu sync.Mutex
}

func (b *captionFixtureBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nativeInputBrain.ThinkStream(ctx, input, opts)
}
func (b *captionFixtureBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]brain.Turn(nil), b.turns...)
	for i := range out {
		out[i].Utterance = out[i].Utterance.Clone()
	}
	return out
}
func (b *captionFixtureBrain) ClearHistory() { b.mu.Lock(); defer b.mu.Unlock(); b.turns = nil }
func (b *captionFixtureBrain) UpdateInputTranscript(id string, epoch uint64, transcript utterance.Transcript) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, turn := range b.turns {
		if turn.Utterance != nil && turn.Utterance.ID == id && turn.Utterance.CaptureEpoch == epoch {
			b.turns[i].Utterance = turn.Utterance.Clone()
			b.turns[i].Utterance.Transcript = transcript
			if transcript.State == utterance.Ready {
				b.turns[i].Content = transcript.Text
			}
			return nil
		}
	}
	return errors.New("stale caption")
}

type captionFixture struct {
	pipeline                  *Pipeline
	responder                 *captionFixtureBrain
	media                     *chatmedia.AudioSession
	ref                       utterance.AudioRef
	started, release, stopped chan struct{}
	updates                   chan events.InputTranscriptUpdated
	saves                     atomic.Int32
}

func newCaptionFixture(t *testing.T) *captionFixture {
	t.Helper()
	f := &captionFixture{media: chatmedia.NewAudioSession(), started: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{}), updates: make(chan events.InputTranscriptUpdated, 4)}
	t.Cleanup(f.media.Close)
	ref, err := f.media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	f.ref = ref
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		close(f.started)
		defer close(f.stopped)
		select {
		case <-f.release:
			_, _ = io.WriteString(w, `{"text":"actual caption"}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
	client, err := nativeaudio.NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	scheduler := nativeaudio.NewScheduler()
	if err := client.BindScheduler(scheduler); err != nil {
		t.Fatal(err)
	}
	f.responder = &captionFixtureBrain{nativeInputBrain: nativeInputBrain{fakeBrain: fakeBrain{chunks: []string{"Hello."}}}}
	bus := events.NewBus()
	events.Subscribe(bus, func(update events.InputTranscriptUpdated) { f.updates <- update })
	policy := &NativePolicy{Controls: "conversation", Transcript: "auto", Media: f.media, Client: client, Scheduler: scheduler, Lifetime: t.Context(), PublishCaption: func(ctx context.Context, commit func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return commit()
	}}
	f.pipeline = &Pipeline{Input: &fixtureNativeInput{input: &utterance.Finalized{ID: "caption-fixture", CaptureEpoch: 5, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}}, Native: policy, Brain: f.responder, Events: bus, OnTurn: func() { f.saves.Add(1) }}
	f.pipeline.SetOutputMuted(true)
	return f
}
func TestNativeAutoCaptionDoesNotBlockAnswerAndUpdatesOnlyOriginalInput(t *testing.T) {
	f := newCaptionFixture(t)
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("caption did not start after answer")
	}
	if history := f.responder.History(); len(history) != 1 || history[0].Utterance.Transcript.State != utterance.Pending {
		t.Fatal(history)
	}
	close(f.release)
	select {
	case update := <-f.updates:
		if update.UtteranceID != "caption-fixture" || update.CaptureEpoch != 5 || update.Transcript.Text != "actual caption" {
			t.Fatal(update)
		}
	case <-time.After(time.Second):
		t.Fatal("caption did not publish")
	}
	if err := f.pipeline.Native.Scheduler.CancelCaption(t.Context()); err != nil {
		t.Fatal(err)
	}
	if history := f.responder.History(); len(history) != 1 || history[0].Content != "actual caption" {
		t.Fatal(history)
	}
	if f.saves.Load() != 2 {
		t.Fatal("caption did not persist attributed update", f.saves.Load())
	}
}
func TestNativeCaptionRetirementJoinsBeforeReleasingItsOwnedReference(t *testing.T) {
	f := newCaptionFixture(t)
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.responder.ClearHistory()
	f.media.Release(f.ref)
	if _, err := f.media.ResolveAudio(t.Context(), f.ref); err != nil {
		t.Fatal("caption reference released during HTTP producer", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.pipeline.Native.Retire(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.media.ResolveAudio(t.Context(), f.ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("caption reference leaked after join", err)
	}
	select {
	case <-f.stopped:
	case <-ctx.Done():
		t.Fatal("HTTP producer did not stop")
	}
	if len(f.updates) != 0 || f.saves.Load() != 1 {
		t.Fatal("retired caption published", f.saves.Load())
	}
}
func TestNewForegroundCancelsCaptionAndMarksItsOriginalInputUnavailable(t *testing.T) {
	f := newCaptionFixture(t)
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-f.started
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.pipeline.prepareNativeForeground(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-f.updates:
		if update.Transcript.State != utterance.Unavailable {
			t.Fatal(update)
		}
	default:
		t.Fatal("cancelled caption stayed pending")
	}
	if _, err := f.media.ResolveAudio(t.Context(), f.ref); err != nil {
		t.Fatal("canonical input lost after caption cancellation", err)
	}
}

type cancellingCaptionBrain struct {
	*captionFixtureBrain
	cancel context.CancelFunc
}

func (b *cancellingCaptionBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	if _, err := b.captionFixtureBrain.ThinkStream(ctx, input, opts); err != nil {
		return nil, err
	}
	b.cancel()
	chunks := make(chan string)
	close(chunks)
	terminal := make(chan brain.StreamResult, 1)
	terminal <- brain.StreamResult{Err: context.Canceled}
	close(terminal)
	return &brain.Stream{Chunks: chunks, Done: terminal}, nil
}
func TestCancelledNativeTurnDoesNotStartOptionalASR(t *testing.T) {
	f := newCaptionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.pipeline.Brain = &cancellingCaptionBrain{captionFixtureBrain: f.responder, cancel: cancel}
	_, _ = f.pipeline.RunTurn(ctx)
	f.pipeline.Native.captionMu.Lock()
	ticket := f.pipeline.Native.caption
	f.pipeline.Native.captionMu.Unlock()
	if ticket != nil {
		t.Fatal("cancelled response created a caption producer")
	}
	select {
	case <-f.started:
		t.Fatal("new ASR started after cancellation")
	case <-time.After(200 * time.Millisecond):
	}
	history := f.responder.History()
	if len(history) != 1 || history[0].Utterance.Transcript.State != utterance.Unavailable {
		t.Fatal("cancelled input stayed pending", history)
	}
}

type waitingNativeInput struct{ started chan struct{} }

func (*waitingNativeInput) Available() bool { return true }
func (p *waitingNativeInput) Start(ctx context.Context) (utterance.Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan utterance.Event)
	done := make(chan struct{})
	close(p.started)
	go func() { defer close(done); defer close(events); <-ctx.Done() }()
	return &waitingNativeSession{events: events, cancel: cancel, done: done}, nil
}

type waitingNativeSession struct {
	events chan utterance.Event
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *waitingNativeSession) Events() <-chan utterance.Event { return s.events }
func (s *waitingNativeSession) Close() error                   { s.cancel(); <-s.done; return nil }
func TestListeningAloneDoesNotCancelOptionalCaption(t *testing.T) {
	f := newCaptionFixture(t)
	if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-f.started
	listener := &waitingNativeInput{started: make(chan struct{})}
	f.pipeline.Input = listener
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := f.pipeline.RunTurn(ctx); finished <- err }()
	select {
	case <-listener.started:
	case <-ctx.Done():
		t.Fatal("next listen did not start")
	}
	select {
	case <-f.stopped:
		t.Fatal("mere listening canceled the caption")
	default:
	}
	close(f.release)
	select {
	case update := <-f.updates:
		if update.Transcript.State != utterance.Ready {
			t.Fatal(update)
		}
	case <-ctx.Done():
		t.Fatal("caption could not finish while listening")
	}
	cancel()
	if err := <-finished; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestClearAndCloseJoinOptionalCaptionBeforeConversationMutation(t *testing.T) {
	for _, clear := range []bool{true, false} {
		t.Run(map[bool]string{true: "clear", false: "close"}[clear], func(t *testing.T) {
			f := newCaptionFixture(t)
			if _, err := f.pipeline.RunTurn(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-f.started:
			case <-time.After(time.Second):
				t.Fatal("caption did not start")
			}
			var err error
			if clear {
				err = f.pipeline.ClearConversation()
			} else {
				err = f.pipeline.CloseWork()
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-f.stopped:
			case <-time.After(time.Second):
				t.Fatal("conversation returned before caption producer stopped")
			}
			update := <-f.updates
			if update.Transcript.State != utterance.Unavailable {
				t.Fatal(update)
			}
			if clear && len(f.responder.History()) != 0 {
				t.Fatal("clear retained canonical turns")
			}
			if clear {
				if _, err := f.media.ResolveAudio(t.Context(), f.ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
					t.Fatal("clear retained caption/canonical audio", err)
				}
			}
			close(f.release)
			if err := f.pipeline.Native.Scheduler.CancelCaption(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case late := <-f.updates:
				t.Fatal("late caption materialized after lifecycle boundary", late)
			default:
			}
		})
	}
}
