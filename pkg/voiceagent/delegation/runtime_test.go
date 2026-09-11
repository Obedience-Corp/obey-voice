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
func (*fakeClient) ExecutionCapability() ExecutionCapability {
	return ExecutionCapability{Local: true}
}
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

func TestSessionCloseDuringAdmissionPublishesUnknownInterruptedOutcome(t *testing.T) {
	f := newFake()
	f.admit = make(chan struct{})
	r := newRuntime(t, f, time.Minute)
	finished := make(chan TerminalOutcome, 1)
	if err := r.AttachConversation(ConversationObserver{
		Admitted: func(string, string) error { return nil },
		Finished: func(outcome TerminalOutcome) error { finished <- outcome; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start("blocked admission"); err != nil {
		t.Fatal(err)
	}
	<-f.started
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case outcome := <-finished:
		if outcome.State != "interrupted" || outcome.Termination != "unknown" || !strings.Contains(outcome.Err, "admission") {
			t.Fatalf("outcome=%+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not publish admission uncertainty")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked on canceled admission")
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

type unverifiedClient struct{ fakeClient }

func (*unverifiedClient) ExecutionCapability() ExecutionCapability {
	return ExecutionCapability{Reason: "paid policy is not verified"}
}

func TestUnverifiedManagedExecutionFailsClosed(t *testing.T) {
	f := unverifiedClient{fakeClient: *newFake()}
	r, err := New(context.Background(), &f, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AttachConversation(ConversationObserver{Admitted: func(string, string) error { return nil }, Finished: func(TerminalOutcome) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start("must be refused"); err == nil || !strings.Contains(err.Error(), "paid policy is not verified") {
		t.Fatalf("start err=%v", err)
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

func TestTerminalObserverReceivesFrozenOutcomeOnceOutsideRuntimeLock(t *testing.T) {
	f := newFake()
	r := newRuntime(t, f, time.Minute)
	outcomes := make(chan TerminalOutcome, 2)
	r.SetTerminalObserver(func(outcome TerminalOutcome) {
		// Pending must be callable here: observers are never invoked under the
		// runtime mutex.
		if _, ok := r.Pending(); !ok {
			t.Errorf("terminal outcome was published before pending delivery")
		}
		outcomes <- outcome
	})
	if _, err := r.Start("retain this original task"); err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.results <- Snapshot{ID: "obey-42", State: "completed", Text: "done"}
	select {
	case outcome := <-outcomes:
		if outcome.Task != "retain this original task" || outcome.SourceRef != "obey-42" || outcome.State != "completed" || outcome.Brief != "done" || outcome.Termination != "confirmed" {
			t.Fatalf("outcome=%+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal observer was not called")
	}
	select {
	case duplicate := <-outcomes:
		t.Fatalf("duplicate terminal outcome: %+v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestConversationAttachmentOwnsOutcomeWithoutLegacyProjection(t *testing.T) {
	f := newFake()
	r := newRuntime(t, f, time.Minute)
	finished := make(chan TerminalOutcome, 1)
	if err := r.AttachConversation(ConversationObserver{
		Admitted: func(ticket, task string) error {
			if ticket == "" || task != "attached task" {
				t.Errorf("admission=%q %q", ticket, task)
			}
			return nil
		},
		Started: func(string) {},
		Finished: func(outcome TerminalOutcome) error {
			finished <- outcome
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start("attached task"); err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.results <- Snapshot{ID: "obey-42", State: "completed", Text: "attached result"}
	select {
	case outcome := <-finished:
		if outcome.Brief != "attached result" || outcome.Elapsed <= 0 || outcome.Ended.Before(outcome.Started) {
			t.Fatalf("outcome=%+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("conversation outcome not published")
	}
	if _, ok := r.Pending(); ok {
		t.Fatal("attached runtime exposed legacy pending projection")
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

func TestNewSessionDoesNotWaitForAdmissionAndOldResultCannotLeak(t *testing.T) {
	f := newFake()
	f.admit = make(chan struct{})
	old := newRuntime(t, f, time.Minute)
	_, err := old.Start("session A task")
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	next := old.NewSession()
	t.Cleanup(func() { _ = next.Close() })
	if next.Ready() != old.Ready() {
		t.Fatal("notification channel changed")
	}
	if _, ok := next.Pending(); ok {
		t.Fatal("new session inherited pending result")
	}
	close(f.admit)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Pending(); ok {
		t.Fatal("late old cancellation/result entered new session")
	}
	if !strings.Contains(next.Status(), "No background") {
		t.Fatal(next.Status())
	}
}
