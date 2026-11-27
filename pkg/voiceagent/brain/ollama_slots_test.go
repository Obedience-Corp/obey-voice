package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/ollama/ollama/api"
)

// slotServer is an Ollama fake. A request whose last message is "work"
// generates until its client goes away, or until finish is closed; any other
// request answers at once. arrivals records each request as it arrives, and
// cancelled each work request whose client went away.
type slotServer struct {
	o         *OllamaBrain
	finish    chan struct{}
	working   chan struct{}
	mu        sync.Mutex
	arrivals  []string
	cancelled int
}

func newSlotServer(t *testing.T, parallel int) *slotServer {
	t.Helper()
	s := &slotServer{finish: make(chan struct{}), working: make(chan struct{}, 8)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		text := req.Messages[len(req.Messages)-1].Content
		s.mu.Lock()
		s.arrivals = append(s.arrivals, req.Model+":"+text)
		s.mu.Unlock()
		answer := "reply"
		if text == "work" {
			s.working <- struct{}{}
			select {
			case <-r.Context().Done():
				s.mu.Lock()
				s.cancelled++
				s.mu.Unlock()
				return
			case <-s.finish:
			}
			answer = "result"
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: api.Message{Role: "assistant", Content: answer}, Done: true, DoneReason: "stop"})
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	s.o = &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", cfg: &config.Config{OllamaHost: srv.URL, OllamaNumParallel: parallel}}
	return s
}

func (s *slotServer) seen() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.arrivals...), s.cancelled
}

// send sends one request with content text to model and returns its answer.
func (s *slotServer) send(ctx context.Context, model, text string) (string, error) {
	stream := false
	var got strings.Builder
	err := s.o.clientChat(ctx, &api.ChatRequest{Model: model, Stream: &stream, Messages: []api.Message{{Role: "user", Content: text}}}, func(resp api.ChatResponse) error {
		got.WriteString(resp.Message.Content)
		return nil
	})
	return got.String(), err
}

type sent struct {
	answer string
	err    error
}

// startWork sends a job's "work" request and waits until the model is
// generating it.
func (s *slotServer) startWork(t *testing.T, ctx context.Context, model string) <-chan sent {
	t.Helper()
	done := make(chan sent, 1)
	go func() {
		answer, err := s.send(job.WithID(ctx, "job-1"), model, "work")
		done <- sent{answer, err}
	}()
	<-s.working
	return done
}

func TestLiveRequestTakesTheSlotFromAJobAndTheJobSendsAgain(t *testing.T) {
	s := newSlotServer(t, 1)
	work := s.startWork(t, t.Context(), "fixture")
	if answer, err := s.send(t.Context(), "fixture", "hello"); err != nil || answer != "reply" {
		t.Fatalf("live answer %q, %v", answer, err)
	}
	<-s.working
	close(s.finish)
	got := <-work
	if got.err != nil || got.answer != "result" {
		t.Fatalf("job answer %q, %v; want one result", got.answer, got.err)
	}
	arrivals, cancelled := s.seen()
	want := []string{"fixture:work", "fixture:hello", "fixture:work"}
	if strings.Join(arrivals, ",") != strings.Join(want, ",") || cancelled != 1 {
		t.Fatalf("arrivals %v, cancelled %d; want %v, 1", arrivals, cancelled, want)
	}
}

func TestJobStoppedWhileWaitingForALiveRequest(t *testing.T) {
	s := newSlotServer(t, 1)
	g := gateFor(s.o.slotKey("fixture"))
	release, err := g.acquireLive(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := s.send(job.WithID(ctx, "job-1"), "fixture", "work")
		done <- err
	}()
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped job returned %v", err)
	}
	if arrivals, _ := s.seen(); len(arrivals) != 0 {
		t.Fatalf("a parked job reached the model: %v", arrivals)
	}
}

func TestRequestForAnotherModelLeavesTheJobRunning(t *testing.T) {
	s := newSlotServer(t, 1)
	work := s.startWork(t, t.Context(), "fixture")
	if answer, err := s.send(t.Context(), "other", "hello"); err != nil || answer != "reply" {
		t.Fatalf("live answer %q, %v", answer, err)
	}
	close(s.finish)
	if got := <-work; got.err != nil || got.answer != "result" {
		t.Fatalf("job answer %q, %v", got.answer, got.err)
	}
	if _, cancelled := s.seen(); cancelled != 0 {
		t.Fatalf("%d job requests cancelled", cancelled)
	}
}

func TestTwoSlotsRunALiveRequestBesideAJob(t *testing.T) {
	s := newSlotServer(t, 2)
	work := s.startWork(t, t.Context(), "fixture")
	if answer, err := s.send(t.Context(), "fixture", "hello"); err != nil || answer != "reply" {
		t.Fatalf("live answer %q, %v", answer, err)
	}
	close(s.finish)
	if got := <-work; got.err != nil || got.answer != "result" {
		t.Fatalf("job answer %q, %v", got.answer, got.err)
	}
	if _, cancelled := s.seen(); cancelled != 0 {
		t.Fatalf("%d job requests cancelled with a free slot", cancelled)
	}
}

func TestWarmupNeverTakesTheSlot(t *testing.T) {
	s := newSlotServer(t, 1)
	work := s.startWork(t, t.Context(), "fixture")
	if _, err := s.send(withModelRequestPurpose(t.Context(), PurposeWarmup), "fixture", "warm"); err != nil {
		t.Fatal(err)
	}
	close(s.finish)
	if got := <-work; got.err != nil || got.answer != "result" {
		t.Fatalf("job answer %q, %v", got.answer, got.err)
	}
	if _, cancelled := s.seen(); cancelled != 0 {
		t.Fatalf("a warmup cancelled %d job requests", cancelled)
	}
}

// A live request whose caller gives up while a cancelled job request is
// still returning leaves the gate as it found it.
func TestLiveRequestCancelledWhileWaiting(t *testing.T) {
	g := &slotGate{changed: make(chan struct{}), capacity: 1}
	cancels := 0
	slot, err := g.acquireJob(t.Context(), 1, func(error) { cancels++ })
	if err != nil {
		t.Fatal(err)
	}
	ctx, give := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := g.acquireLive(ctx, 1)
		done <- err
	}()
	waitFor(t, g, func() bool { return g.liveWaiting == 1 && slot.preempted })
	give()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("live request returned %v", err)
	}
	g.mu.Lock()
	waiting, running := g.liveWaiting, g.liveRunning
	g.mu.Unlock()
	if waiting != 0 || running != 0 || cancels != 1 {
		t.Fatalf("waiting %d, running %d, cancels %d", waiting, running, cancels)
	}
	if !g.releaseJob(slot) {
		t.Fatal("the job request was not marked preempted")
	}
}

// A job request that delivered its answer before the live request arrived is
// pinned: it is not cancelled and its answer stands.
func TestJobThatAlreadyAnsweredIsNotCancelled(t *testing.T) {
	g := &slotGate{changed: make(chan struct{}), capacity: 1}
	cancels := 0
	slot, err := g.acquireJob(t.Context(), 1, func(error) { cancels++ })
	if err != nil {
		t.Fatal(err)
	}
	if !g.claimDelivery(slot) {
		t.Fatal("delivery refused before any live request")
	}
	release, err := g.acquireLive(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if cancels != 0 || g.releaseJob(slot) {
		t.Fatalf("a pinned job request was cancelled (%d)", cancels)
	}
}

// A job request cancelled just as its answer arrives drops that answer, so
// the request sent again is the only one its caller sees.
func TestJobAnswerArrivingAfterPreemptionIsDropped(t *testing.T) {
	g := &slotGate{changed: make(chan struct{}), capacity: 1}
	slot, err := g.acquireJob(t.Context(), 1, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	live := make(chan func(), 1)
	go func() {
		release, _ := g.acquireLive(t.Context(), 1)
		live <- release
	}()
	waitFor(t, g, func() bool { return slot.preempted })
	if g.claimDelivery(slot) {
		t.Fatal("a preempted request delivered its answer")
	}
	if !g.releaseJob(slot) {
		t.Fatal("release did not report the preemption")
	}
	(<-live)()
}

// waitFor yields until cond holds under the gate's lock.
func waitFor(t *testing.T, g *slotGate, cond func() bool) {
	t.Helper()
	for {
		g.mu.Lock()
		held := cond()
		g.mu.Unlock()
		if held {
			return
		}
		if t.Context().Err() != nil {
			t.Fatal("condition never held")
		}
		runtime.Gosched()
	}
}
