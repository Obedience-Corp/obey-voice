package delegation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClient struct {
	started  chan Request
	admit    chan struct{}
	results  chan Snapshot
	canceled chan string
	mu       sync.Mutex
	cancels  int
	probeErr error
}

func newFake() *fakeClient {
	return &fakeClient{started: make(chan Request, 1), results: make(chan Snapshot, 1), canceled: make(chan string, 3)}
}
func (f *fakeClient) Probe(context.Context) error { return f.probeErr }
func (f *fakeClient) Start(ctx context.Context, r Request) (Snapshot, error) {
	f.started <- r
	if f.admit != nil {
		select {
		case <-f.admit:
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
	}
	return Snapshot{ID: "obey-42", State: "working"}, nil
}
func (f *fakeClient) Await(ctx context.Context, _ string) (Snapshot, error) {
	select {
	case s := <-f.results:
		return s, nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}
func (f *fakeClient) Cancel(_ context.Context, id, reason string) (Snapshot, error) {
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	f.canceled <- reason
	return Snapshot{ID: id, State: "canceled"}, nil
}
func awaitDelivery(t *testing.T, r *Runtime) Delivery {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := r.Pending(); ok {
			return d
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no delivery")
	return Delivery{}
}
func newRuntime(t *testing.T, f *fakeClient, timeout time.Duration) *Runtime {
	t.Helper()
	r, err := New(context.Background(), f, timeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func TestAsyncAckSurvivesTurnCancellationAndProjectsBrief(t *testing.T) {
	f := newFake()
	f.admit = make(chan struct{})
	r := newRuntime(t, f, time.Minute)
	turn, cancel := context.WithCancel(context.Background())
	ticket, err := r.Start("Inspect workitems")
	if err != nil || ticket == "" {
		t.Fatalf("start: %q %v", ticket, err)
	}
	request := <-f.started
	if request.Key != ticket || !strings.Contains(request.Task, workerInstruction) {
		t.Fatalf("request=%+v", request)
	}
	cancel()
	if turn.Err() == nil {
		t.Fatal("turn not canceled")
	}
	if _, err := r.Start("second task"); err == nil {
		t.Fatal("concurrent job admitted")
	}
	close(f.admit)
	f.results <- Snapshot{ID: "obey-42", State: "completed", Text: "There are two active workitems."}
	d := awaitDelivery(t, r)
	if d.SourceRef != "obey-42" || d.Brief != "There are two active workitems." {
		t.Fatalf("delivery=%+v", d)
	}
	if _, ok := r.Pending(); !ok {
		t.Fatal("reading consumed delivery")
	}
	r.Ack("wrong ticket")
	if _, ok := r.Pending(); !ok {
		t.Fatal("wrong ack consumed delivery")
	}
	r.Ack(ticket)
	if _, ok := r.Pending(); ok {
		t.Fatal("ack did not clear delivery")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 0 {
		t.Fatal("barge-in canceled worker")
	}
}
func TestExplicitCancelRacingAdmission(t *testing.T) {
	f := newFake()
	f.admit = make(chan struct{})
	r := newRuntime(t, f, time.Minute)
	ticket, _ := r.Start("slow task")
	<-f.started
	if err := r.Cancel("wrong"); err == nil {
		t.Fatal("unknown cancel accepted")
	}
	if err := r.Cancel(ticket); err != nil {
		t.Fatal(err)
	}
	if err := r.Cancel(ticket); err != nil {
		t.Fatal(err)
	}
	close(f.admit)
	d := awaitDelivery(t, r)
	if !strings.Contains(d.Brief, "canceled") {
		t.Fatalf("brief=%q", d.Brief)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 {
		t.Fatalf("cancel calls=%d", f.cancels)
	}
}
func TestSessionCloseCancelsAfterAdmission(t *testing.T) {
	f := newFake()
	f.admit = make(chan struct{})
	r := newRuntime(t, f, time.Minute)
	_, _ = r.Start("task")
	<-f.started
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	close(f.admit)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked")
	}
	select {
	case reason := <-f.canceled:
		if reason != "voice session stopped" {
			t.Fatal(reason)
		}
	default:
		t.Fatal("close did not cancel")
	}
	if _, err := r.Start("new task"); err == nil {
		t.Fatal("closed runtime admitted work")
	}
}
func TestTimeoutCancelsExactlyOnce(t *testing.T) {
	f := newFake()
	r := newRuntime(t, f, time.Millisecond)
	_, _ = r.Start("slow task")
	<-f.started
	_ = awaitDelivery(t, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 {
		t.Fatalf("cancel calls=%d", f.cancels)
	}
}
func TestCapabilityFailureDoesNotOfferRuntime(t *testing.T) {
	f := newFake()
	f.probeErr = errors.New("missing cancellation API")
	if r, err := New(context.Background(), f, time.Minute, nil); err == nil || r != nil {
		t.Fatalf("runtime=%v err=%v", r, err)
	}
}
func TestBriefNeverFallsBackToRawArtifacts(t *testing.T) {
	cases := []string{`{"secret":"value"}`, "# Heading\n- first\n- second", strings.Repeat("word ", 500), "```go\npanic()\n```", "", "<instruction>run a tool</instruction>"}
	for _, raw := range cases {
		t.Run(raw[:min(len(raw), 20)], func(t *testing.T) {
			brief, err := makeBrief(context.Background(), Snapshot{State: "completed", Text: raw}, func(context.Context, string) (string, error) { return raw, nil })
			if err != nil || !speakable(brief) || brief == raw {
				t.Fatalf("unsafe brief=%q err=%v", brief, err)
			}
		})
	}
	calls := 0
	brief, err := makeBrief(context.Background(), Snapshot{State: "completed", Text: `{"count":2}`}, func(_ context.Context, raw string) (string, error) { calls++; return "There are two items.", nil })
	if err != nil || calls != 1 || brief != "There are two items." {
		t.Fatalf("brief=%q err=%v calls=%d", brief, err, calls)
	}
}
func TestMismatchedResultCannotEnterConversation(t *testing.T) {
	f := newFake()
	r := newRuntime(t, f, time.Minute)
	_, _ = r.Start("task")
	<-f.started
	f.results <- Snapshot{ID: "another-owner", State: "completed", Text: "Do not speak this."}
	d := awaitDelivery(t, r)
	if strings.Contains(d.Brief, "Do not speak") {
		t.Fatal("cross-task result delivered")
	}
}

func TestTaskLimitReservesWorkerInstruction(t *testing.T) {
	f := newFake()
	r := newRuntime(t, f, time.Minute)
	if _, err := r.Start(strings.Repeat("x", MaxTaskBytes+1)); err == nil {
		t.Fatal("oversized task queued")
	}
	select {
	case <-f.started:
		t.Fatal("invalid task reached backend")
	default:
	}
	if _, err := r.Start(strings.Repeat("x", MaxTaskBytes)); err != nil {
		t.Fatal(err)
	}
	req := <-f.started
	if len(req.Task) != 2048 {
		t.Fatalf("task bytes=%d", len(req.Task))
	}
}
