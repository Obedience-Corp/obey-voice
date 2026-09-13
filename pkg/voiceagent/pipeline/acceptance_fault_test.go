package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type acceptanceWorker struct {
	calls atomic.Int32
	tools atomic.Int32
}

type acceptanceCompletion struct {
	*handoffCompletionBrain
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *acceptanceCompletion) Complete(ctx context.Context, req brain.CompletionRequest) (brain.CompletionEnvelope, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return brain.CompletionEnvelope{}, ctx.Err()
	}
	return b.handoffCompletionBrain.Complete(ctx, req)
}

func (w *acceptanceWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("worker inference was not expected")
}
func (w *acceptanceWorker) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	w.calls.Add(1)
	if opts.OnToolStart != nil {
		call := brain.ToolCall{ID: "acceptance-tool", Name: "list_files", Kind: brain.KindListDir, Summary: "."}
		opts.OnToolStart(call)
		w.tools.Add(1)
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: call, Preview: "fixture"})
		}
	}
	return "unexpected worker execution", nil
}
func (w *acceptanceWorker) ClearHistory()            {}
func (w *acceptanceWorker) History() []brain.Turn    { return nil }
func (w *acceptanceWorker) LoadHistory([]brain.Turn) {}

func TestAcceptanceConcurrentPublishedResultsDeliverOnce(t *testing.T) {
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	completion := &acceptanceCompletion{handoffCompletionBrain: &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{}}, entered: make(chan struct{}), release: make(chan struct{})}
	worker := &acceptanceWorker{}
	p := &Pipeline{Brain: completion, TTS: &fakeTTS{delay: time.Millisecond}, Player: &fencedHandoffPlayer{}, Events: events.NewBus()}
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("acceptance", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	c, scope := p.WorkCoordinator(), p.WorkSnapshot().Scope
	keys := make([]work.JobKey, 2)
	for i := range keys {
		var err error
		keys[i], err = c.Register(scope, work.Admission{JobID: "concurrent-" + string(rune('a'+i)), Task: "task"})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key work.JobKey) {
			defer wg.Done()
			<-start
			if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "result-" + string(rune('a'+i))}); err != nil {
				t.Errorf("publish %s: %v", key.JobID, err)
			}
		}(i, key)
	}
	close(start)
	wg.Wait()

	firstDone := make(chan error, 1)
	go func() { firstDone <- p.RunBackgroundTurn(context.Background()) }()
	select {
	case <-completion.entered:
	case <-time.After(time.Second):
		t.Fatal("host did not reach completion provider after claiming result")
	}
	secondKey, err := c.Register(scope, work.Admission{JobID: "concurrent-c", Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(secondKey, work.Outcome{Scope: scope, JobID: secondKey.JobID, Status: work.OutcomeSucceeded, Text: "result-c"}); err != nil {
		t.Fatal(err)
	}
	competing := make(chan error, 1)
	go func() { competing <- p.RunBackgroundTurn(context.Background()) }()
	select {
	case err := <-competing:
		if err == nil {
			t.Fatal("competing host completion unexpectedly succeeded while first lease was active")
		}
	case <-time.After(time.Second):
		t.Fatal("competing host completion remained blocked")
	}
	close(completion.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal("first host completion:", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first host completion did not finish")
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal("second result delivery:", err)
	}
	snap := c.Snapshot(scope)
	if len(snap.Pending) != 0 || len(snap.Communicated) != 3 {
		t.Fatalf("concurrent delivery snapshot=%+v", snap)
	}
	expected := map[string]bool{"concurrent-a": false, "concurrent-b": false, "concurrent-c": false}
	for _, r := range snap.Communicated {
		already, ok := expected[r.Outcome.JobID]
		if !ok || already {
			t.Fatalf("duplicate/unexpected outcome: %+v", r)
		}
		expected[r.Outcome.JobID] = true
		receipt := r.LastAttempt.Receipt
		if receipt.Mode != "voice" || !receipt.PlaybackComplete || !receipt.Audible || !receipt.ResponseCommitted {
			t.Fatalf("incomplete delivery receipt: %+v", receipt)
		}
		count := 0
		for _, turn := range completion.History() {
			if turn.Role == "assistant" && strings.Contains(turn.Content, r.Outcome.Text) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("result %s history count=%d", r.Outcome.JobID, count)
		}
	}
	if completion.requestCount() != 2 || worker.calls.Load() != 0 || worker.tools.Load() != 0 {
		t.Fatalf("completion calls=%d worker calls=%d tool calls=%d", completion.requestCount(), worker.calls.Load(), worker.tools.Load())
	}
}

func TestAcceptanceRestoreHeldActionThenHostDelivery(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "model")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	completion := &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{}}
	worker := &acceptanceWorker{}
	p := &Pipeline{Brain: completion, TTS: &fakeTTS{delay: time.Millisecond}, Player: &fencedHandoffPlayer{}, Events: events.NewBus()}
	a := NewAgency(p, jobs, worker, &config.Config{}, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork(sess.ID, nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	source := p.WorkSnapshot().Scope
	c := p.WorkCoordinator()
	// Route through Agency so the worker really runs and produces the
	// canonical result that is later restored. A direct Register/Publish pair
	// would only test coordinator persistence and could miss worker reruns.
	a.handleSpawn(context.Background(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "restore task"})
	deadline := time.Now().Add(time.Second)
	var result work.ResultView
	for {
		pending := c.Pending(source)
		if len(pending) == 1 && pending[0].Outcome.Text == "unexpected worker execution" {
			result = pending[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agency worker did not publish result: %+v", pending)
		}
		time.Sleep(time.Millisecond)
	}
	if worker.calls.Load() != 1 {
		t.Fatalf("agency worker calls=%d, want one execution", worker.calls.Load())
	}
	saved := p.WorkSnapshot()
	if err := store.BeginWork(sess, saved, nil); err != nil {
		t.Fatal("persist initial work:", err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil || loaded.Work == nil {
		t.Fatalf("load initial work: %v", err)
	}
	if err := p.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if err := p.EnableConversationWork(sess.ID, loaded.Work, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	current := p.WorkSnapshot().Scope
	if err := store.BeginWork(loaded, p.WorkSnapshot(), &source); err != nil {
		t.Fatal("persist restored work:", err)
	}
	got := p.WorkCoordinator().Pending(current)
	if len(got) != 1 || got[0].State != work.Held || got[0].Outcome.ResultID != result.Outcome.ResultID {
		t.Fatalf("restored pending=%+v", got)
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if completion.requestCount() != 0 || len(p.WorkCoordinator().Pending(current)) != 1 {
		t.Fatal("restored result auto-delivered before explicit action")
	}
	if err := p.WorkResultAction(current, result.Outcome.ResultID, "replay"); err != nil {
		t.Fatal(err)
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWork(loaded, p.WorkSnapshot()); err != nil {
		t.Fatal("persist delivered work:", err)
	}
	durable, err := store.Load(sess.ID)
	if err != nil || durable.Work == nil || len(durable.Work.Communicated) != 1 {
		t.Fatalf("durable delivered session=%+v err=%v", durable, err)
	}
	firstAttempt := finalAttemptID(p.WorkSnapshot())
	if err := p.WorkResultAction(current, result.Outcome.ResultID, "replay"); err != nil {
		t.Fatal("replay communicated result:", err)
	}
	if err := p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal("deliver replayed result:", err)
	}
	if err := store.SaveWork(loaded, p.WorkSnapshot()); err != nil {
		t.Fatal("persist replayed work:", err)
	}
	durable, err = store.Load(sess.ID)
	if err != nil || durable.Work == nil || len(durable.Work.Communicated) != 1 {
		t.Fatalf("durable replayed work=%+v err=%v", durable, err)
	}
	final := p.WorkSnapshot()
	if len(final.Pending) != 0 || len(final.Communicated) != 1 || final.Communicated[0].Outcome.ResultID != result.Outcome.ResultID || final.Communicated[0].Outcome.Text != "unexpected worker execution" {
		t.Fatalf("restored delivery=%+v", final)
	}
	if firstAttempt == "" || finalAttemptID(final) == firstAttempt || worker.calls.Load() != 1 || worker.tools.Load() != 1 || completion.requestCount() != 2 {
		t.Fatalf("replay identity/calls first=%q final=%q worker=%d tools=%d completion=%d", firstAttempt, finalAttemptID(final), worker.calls.Load(), worker.tools.Load(), completion.requestCount())
	}
}

func finalAttemptID(s work.Snapshot) string {
	if len(s.Communicated) == 0 || s.Communicated[len(s.Communicated)-1].LastAttempt == nil {
		return ""
	}
	return s.Communicated[len(s.Communicated)-1].LastAttempt.Receipt.AttemptID
}

func TestAcceptanceExplicitPaidWorkerIsRejectedBeforeAgencyExecution(t *testing.T) {
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	cfg := &config.Config{VoiceWorkerProvider: "claude", VoiceWorkerModel: "paid-model"}
	worker := &acceptanceWorker{}
	p := &Pipeline{Brain: &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{}}, Events: events.NewBus()}
	a := NewAgency(p, jobs, worker, cfg, nil)
	a.Wire()
	p.Agency = a
	if err := p.EnableConversationWork("paid-worker", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	a.handleSpawn(context.Background(), nil, nil, RouteDecision{Kind: RouteSpawn, Transcript: "paid worker must be rejected"})
	if worker.calls.Load() != 0 || len(p.Workers()) != 0 {
		t.Fatalf("explicit paid worker executed or admitted: calls=%d workers=%+v", worker.calls.Load(), p.Workers())
	}
}
