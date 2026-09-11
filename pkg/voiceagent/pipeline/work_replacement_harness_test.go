package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type blockingWorker struct {
	started chan struct{}
	release chan struct{}
	reply   string
}

type stagedWorker struct {
	started chan struct{}
	release chan struct{}
	reply   string
	calls   atomic.Int32
}

func (w *stagedWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *stagedWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	if w.calls.Add(1) == 1 {
		return w.reply, nil
	}
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	select {
	case <-w.release:
		return w.reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (*stagedWorker) ClearHistory()            {}
func (*stagedWorker) History() []brain.Turn    { return nil }
func (*stagedWorker) LoadHistory([]brain.Turn) {}

type closableBrain struct {
	*lifecycleWorker
	closed chan struct{}
}

func (b *closableBrain) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func (w *blockingWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *blockingWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	select {
	case <-w.release:
		return w.reply, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (*blockingWorker) ClearHistory()            {}
func (*blockingWorker) History() []brain.Turn    { return nil }
func (*blockingWorker) LoadHistory([]brain.Turn) {}

type managedHarnessClient struct {
	started  chan delegation.Request
	result   chan delegation.Snapshot
	canceled chan struct{}
}

func (c *managedHarnessClient) Probe(context.Context) error { return nil }
func (*managedHarnessClient) ExecutionCapability() delegation.ExecutionCapability {
	return delegation.ExecutionCapability{Local: true}
}
func (c *managedHarnessClient) Start(_ context.Context, req delegation.Request) (delegation.Snapshot, error) {
	c.started <- req
	return delegation.Snapshot{ID: req.Key, State: "working"}, nil
}
func (c *managedHarnessClient) Await(ctx context.Context, id string) (delegation.Snapshot, error) {
	select {
	case s := <-c.result:
		return s, nil
	case <-ctx.Done():
		return delegation.Snapshot{}, ctx.Err()
	case <-c.canceled:
		return delegation.Snapshot{ID: id, State: "canceled"}, nil
	}
}
func (c *managedHarnessClient) Cancel(_ context.Context, id, _ string) (delegation.Snapshot, error) {
	select {
	case <-c.canceled:
	default:
		close(c.canceled)
	}
	return delegation.Snapshot{ID: id, State: "canceled"}, nil
}

type managedHarnessBrain struct {
	brain.Provider
	runtime      *delegation.Runtime
	closed       chan struct{}
	borrowed     bool
	rejectAttach bool
}

func (b *managedHarnessBrain) DetachDelegation() *delegation.Runtime {
	r := b.runtime
	b.runtime = nil
	return r
}

func (b *managedHarnessBrain) AttachDelegation(r *delegation.Runtime) error {
	if b.rejectAttach {
		return errors.New("test attach rejection")
	}
	b.runtime = r
	b.borrowed = true
	return nil
}
func (b *managedHarnessBrain) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	if b.runtime != nil && !b.borrowed {
		return b.runtime.Close()
	}
	return nil
}

func waitWorkPending(t *testing.T, c *work.Coordinator, s work.WorkScope, want int) []work.ResultView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := c.Pending(s); len(got) >= want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending did not reach %d: %v", want, c.Pending(s))
	return nil
}

func TestAgencyRunningAndQueuedSurviveBrainReplacementAndUseNextConfig(t *testing.T) {
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(1))
	t.Cleanup(jobs.Close)
	oldWorker := &stagedWorker{started: make(chan struct{}), release: make(chan struct{}), reply: "old"}
	nextWorker := &blockingWorker{started: make(chan struct{}), release: make(chan struct{}), reply: "next"}
	oldBrain := &closableBrain{lifecycleWorker: &lifecycleWorker{reply: "old-brain"}, closed: make(chan struct{})}
	nextBrain := &closableBrain{lifecycleWorker: &lifecycleWorker{reply: "next-brain"}, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: oldBrain}
	t.Cleanup(func() { _ = p.CloseWork() })
	a := NewAgency(p, jobs, oldWorker, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "old"}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("s", nil, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "old"}); err != nil {
		t.Fatal(err)
	}
	c := p.WorkCoordinator()
	scope := p.WorkSnapshot().Scope
	a.handleSpawn(context.Background(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "first"})
	waitWorkPending(t, c, scope, 1)
	a.handleSpawn(context.Background(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "second"})
	select {
	case <-oldWorker.started:
	case <-time.After(time.Second):
		t.Fatal("second Agency job did not start")
	}
	if err := p.ReplaceConversationBrain(nextBrain, nextWorker, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "next"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldBrain.closed:
	case <-time.After(time.Second):
		t.Fatal("old brain was not closed")
	}
	close(oldWorker.release)
	got := waitWorkPending(t, c, scope, 2)
	for _, result := range got {
		if result.Outcome.Origin != "agency" || result.Outcome.Model != "old" {
			t.Fatalf("frozen provenance=%+v", result.Outcome)
		}
	}
	a.handleSpawn(context.Background(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "future"})
	close(nextWorker.release)
	got = waitWorkPending(t, c, scope, 3)
	foundNext := false
	for _, result := range got {
		if result.Outcome.Model == "next" {
			foundNext = true
		}
	}
	if !foundNext {
		t.Fatalf("future provenance not found: %+v", got)
	}
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedReadyResultSurvivesReplacementAndOldRuntimeCloses(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	client2 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime2, err := delegation.New(context.Background(), client2, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: runtime, closed: make(chan struct{})}
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: runtime2, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("s", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	ticket, err := runtime.Start("managed ready")
	if err != nil {
		t.Fatal(err)
	}
	<-client.started
	scope := p.WorkSnapshot().Scope
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next"}, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "next"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.closed:
	case <-time.After(time.Second):
		t.Fatal("old brain was not retired")
	}
	select {
	case <-client.canceled:
		t.Fatal("replacement canceled the old managed task")
	default:
	}
	client.result <- delegation.Snapshot{ID: ticket, State: "completed", Text: "ready"}
	waitWorkPending(t, p.WorkCoordinator(), scope, 1)
	if _, err := runtime2.Start("next managed"); err != nil {
		t.Fatal(err)
	}
	nextRequest := <-client2.started
	client2.result <- delegation.Snapshot{ID: nextRequest.Key, State: "completed", Text: "next"}
	got := waitWorkPending(t, p.WorkCoordinator(), scope, 2)
	if got[1].Outcome.Origin != "managed" {
		t.Fatalf("future managed origin=%+v", got[1].Outcome)
	}
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCompletedPendingSurvivesReplacementUnchanged(t *testing.T) {
	client1 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	client2 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime1, err := delegation.New(context.Background(), client1, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime2, err := delegation.New(context.Background(), client2, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: runtime1, closed: make(chan struct{})}
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: runtime2, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	cfg := &config.Config{VoiceWorkerProvider: "ollama", DelegationProfile: "profile-old", BrainProvider: "ollama", OllamaModel: "summarizer-old"}
	if err := p.EnableConversationWork("s", nil, cfg); err != nil {
		t.Fatal(err)
	}
	ticket, err := runtime1.Start("completed before replacement")
	if err != nil {
		t.Fatal(err)
	}
	request := <-client1.started
	if request.Key != ticket {
		t.Fatalf("request key=%q ticket=%q", request.Key, ticket)
	}
	client1.result <- delegation.Snapshot{ID: request.Key, State: "completed", Text: "ready result"}
	scope := p.WorkSnapshot().Scope
	before := waitWorkPending(t, p.WorkCoordinator(), scope, 1)
	saved := before[0]
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next"}, &config.Config{VoiceWorkerProvider: "ollama", DelegationProfile: "profile-next", BrainProvider: "ollama", OllamaModel: "summarizer-next"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.closed:
	case <-time.After(time.Second):
		t.Fatal("old brain was not closed")
	}
	select {
	case req := <-client2.started:
		t.Fatalf("replacement dispatched unexpectedly: %+v", req)
	default:
	}
	after := waitWorkPending(t, p.WorkCoordinator(), scope, 1)
	if len(after) != 1 || !reflect.DeepEqual(after[0], saved) {
		t.Fatalf("pending changed across replacement: before=%+v after=%+v", saved, after)
	}
	if after[0].Outcome.Origin != "managed" || after[0].Outcome.Config["profile"] != "profile-old" {
		t.Fatalf("managed provenance changed: %+v", after[0].Outcome)
	}
}

func TestManagedExplicitCancelPublishesConfirmedTerminalOutcome(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	client2 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime2, err := delegation.New(context.Background(), client2, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "unused"}, runtime: runtime, closed: make(chan struct{})}
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: runtime2, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("s", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	ticket, err := runtime.Start("cancel me")
	if err != nil {
		t.Fatal(err)
	}
	<-client.started
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next"}, &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "next"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.CancelWork(ticket); err != nil {
		t.Fatal(err)
	}
	got := waitWorkPending(t, p.WorkCoordinator(), p.WorkSnapshot().Scope, 1)
	if got[0].Outcome.Status != work.OutcomeCancelled {
		t.Fatalf("cancel outcome=%+v", got[0].Outcome)
	}
	if got[0].Outcome.Termination != "confirmed" {
		t.Fatalf("cancel termination=%q", got[0].Outcome.Termination)
	}
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedClearAndResumeRotatesClosedRuntime(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 2), result: make(chan delegation.Snapshot, 2), canceled: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	brain := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "managed"}, runtime: runtime, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: brain}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	cfg := &config.Config{VoiceWorkerProvider: "ollama", DelegationProfile: "local"}
	if err := p.EnableConversationWork("s", nil, cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	firstRuntime := brain.runtime
	ticket, err := firstRuntime.Start("after clear")
	if err != nil {
		t.Fatal(err)
	}
	request := <-client.started
	if request.Key != ticket {
		t.Fatalf("request key=%q ticket=%q", request.Key, ticket)
	}
	client.result <- delegation.Snapshot{ID: request.Key, State: "completed", Text: "clear result"}
	waitWorkPending(t, p.WorkCoordinator(), p.WorkSnapshot().Scope, 1)
	saved := p.WorkSnapshot()
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if err := p.EnableConversationWork("s", &saved, cfg); err != nil {
		t.Fatal(err)
	}
	if brain.runtime == firstRuntime {
		t.Fatal("resume reused closed managed runtime")
	}
	secondTicket, err := brain.runtime.Start("after resume")
	if err != nil {
		t.Fatal(err)
	}
	request = <-client.started
	if request.Key != secondTicket {
		t.Fatalf("request key=%q ticket=%q", request.Key, secondTicket)
	}
	client.result <- delegation.Snapshot{ID: request.Key, State: "completed", Text: "resume result"}
	waitWorkPending(t, p.WorkCoordinator(), p.WorkSnapshot().Scope, 2)
}

func TestFinalCloseStopsOldAndReplacementManagedRuntimes(t *testing.T) {
	client1 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	client2 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime1, err := delegation.New(context.Background(), client1, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime2, err := delegation.New(context.Background(), client2, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: runtime1, closed: make(chan struct{})}
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: runtime2, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("s", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime1.Start("old running"); err != nil {
		t.Fatal(err)
	}
	<-client1.started
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime2.Start("next running"); err != nil {
		t.Fatal(err)
	}
	<-client2.started
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client1.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("old runtime was not canceled")
	}
	select {
	case <-client2.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement runtime was not canceled")
	}
}

func TestReplacementAttachFailureLeavesOldBrainUsable(t *testing.T) {
	client := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime, err := delegation.New(context.Background(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "old"}, runtime: runtime, closed: make(chan struct{})}
	p := &Pipeline{Events: events.NewBus(), Brain: old}
	jobs := job.NewTable(context.Background())
	t.Cleanup(func() { jobs.Close(); _ = p.CloseWork() })
	a := NewAgency(p, jobs, &lifecycleWorker{reply: "agency"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("s", nil, &config.Config{VoiceWorkerProvider: "ollama"}); err != nil {
		t.Fatal(err)
	}
	client2 := &managedHarnessClient{started: make(chan delegation.Request, 1), result: make(chan delegation.Snapshot, 1), canceled: make(chan struct{})}
	runtime2, err := delegation.New(context.Background(), client2, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := &managedHarnessBrain{Provider: &lifecycleWorker{reply: "next"}, runtime: runtime2, closed: make(chan struct{}), rejectAttach: true}
	if err := p.ReplaceConversationBrain(next, &lifecycleWorker{reply: "next"}, &config.Config{VoiceWorkerProvider: "ollama"}, nil); err == nil {
		t.Fatal("replacement attach unexpectedly succeeded")
	}
	if p.Brain != old {
		t.Fatal("failed replacement partially swapped the foreground brain")
	}
	if !runtime.Enabled() {
		t.Fatal("old runtime was disabled by failed replacement")
	}
}
