package pipeline

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type slowWorker struct {
	started chan struct{}
	done    chan struct{}
	reply   string
}

func (w *slowWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{w.reply}}).ThinkStream(ctx, input, opts)
}

func (w *slowWorker) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	close(w.started)
	select {
	case <-w.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return w.reply, nil
}

func (w *slowWorker) ClearHistory()        {}
func (w *slowWorker) History() []brain.Turn { return nil }
func (w *slowWorker) LoadHistory([]brain.Turn) {}

func TestSpawnAckBeforeWorkerFinishes(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	worker := &slowWorker{
		started: make(chan struct{}),
		done:    make(chan struct{}),
		reply:   "plain result",
	}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{
		TTS:    provider,
		Player: player,
		Events: events.NewBus(),
	}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		agency.HandleRoute(ctx, turn, metrics, RouteDecision{
			Kind:       RouteSpawn,
			Transcript: "filter design work items",
			Source:     "test",
		})
	}()

	deadline := time.After(2 * time.Second)
	for {
		calls := provider.Calls()
		if len(calls) > 0 {
			if !strings.Contains(strings.ToLower(calls[0]), "working") {
				t.Fatalf("first spoken line = %q, want ack", calls[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("ack was not spoken before worker finished")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(worker.done)
	wg.Wait()
}

func TestWorkerMarkdownNotSpokenRaw(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	worker := &fakeBrain{chunks: []string{"```json\n{\"x\":1}\n```\n\n**Done.**"}}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()

	done := make(chan struct{})
	jobs.OnDone(func(string, job.State, string, error) { close(done) })

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteSpawn,
		Transcript: "run camp workitem",
		Source:     "test",
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish")
	}

	// Allow async summary flush.
	time.Sleep(50 * time.Millisecond)
	for _, line := range provider.Calls() {
		if strings.Contains(line, "```") || strings.Contains(line, "**") {
			t.Fatalf("markdown spoken raw: %v", provider.Calls())
		}
	}
}

func TestStopPlaybackDoesNotAbortJob(t *testing.T) {
	player := newFakePlayer(50 * time.Millisecond)
	defer player.Close()

	var workerRunning atomic.Bool
	worker := &slowWorker{
		started: make(chan struct{}),
		done:    make(chan struct{}),
		reply:   "ok",
	}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()

	finished := make(chan job.State, 1)
	jobs.OnDone(func(_ string, state job.State, _ string, _ error) {
		finished <- state
	})

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteSpawn,
		Transcript: "long task",
		Source:     "test",
	})

	<-worker.started
	workerRunning.Store(true)
	p.StopPlayback()
	if !workerRunning.Load() {
		t.Fatal("job should still be running after StopPlayback")
	}

	close(worker.done)
	select {
	case state := <-finished:
		if state != job.StateDone {
			t.Fatalf("terminal state = %q, want done", state)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job never finished")
	}
}

func TestQueuedJobSummariesFlushFIFOWhenIdle(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	worker := &seqWorker{replies: []string{"first job result", "second job result"}}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{
		TTS:    provider,
		Player: player,
		Events: events.NewBus(),
	}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()

	// A foreground turn blocks idle delivery the same way playback would.
	p.incActiveTurn()

	finished := make(chan struct{}, 2)
	p.Events.SubscribeAll(func(e events.Event) {
		if _, ok := e.(events.JobFinished); ok {
			finished <- struct{}{}
		}
	})

	ctx := context.Background()
	spawn := func(task string) {
		metrics := newTurnMetrics()
		turn := p.newTurnConductor(metrics)
		agency.HandleRoute(ctx, turn, metrics, RouteDecision{
			Kind:       RouteSpawn,
			Transcript: task,
			Source:     "test",
		})
	}
	spawn("first task")
	spawn("second task")

	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("jobs did not finish while the foreground turn was held")
		}
	}

	agency.mu.Lock()
	nPending := len(agency.pending)
	ids := make([]string, nPending)
	texts := make([]string, nPending)
	for i, d := range agency.pending {
		ids[i] = d.JobID
		texts[i] = d.Text
	}
	agency.mu.Unlock()
	if nPending != 2 {
		t.Fatalf("pending deliveries = %d (%v %v), want 2 (both held until AfterTurn)", nPending, ids, texts)
	}
	if texts[0] != "first job result" || texts[1] != "second job result" {
		t.Fatalf("pending FIFO = %v, want [first job result second job result]", texts)
	}
	if ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("pending job ids = %v, want two distinct identities", ids)
	}

	for _, line := range provider.Calls() {
		if strings.Contains(line, "first job result") || strings.Contains(line, "second job result") {
			t.Fatalf("summary spoken before idle: %v", provider.Calls())
		}
	}

	p.decActiveTurn()

	deadline := time.After(3 * time.Second)
	for {
		var first, second int
		for i, line := range provider.Calls() {
			if strings.Contains(line, "first job result") {
				first = i + 1
			}
			if strings.Contains(line, "second job result") {
				second = i + 1
			}
		}
		if first > 0 && second > 0 {
			if first > second {
				t.Fatalf("spoken order = %v, want first job before second", provider.Calls())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("spoken = %v, want both job summaries in FIFO order", provider.Calls())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type seqWorker struct {
	mu      sync.Mutex
	replies []string
	i       int
}

func (w *seqWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	text, err := w.ThinkFull(ctx, input, opts)
	if err != nil {
		return nil, err
	}
	return (&fakeBrain{chunks: []string{text}}).ThinkStream(ctx, input, opts)
}

func (w *seqWorker) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.i >= len(w.replies) {
		return "", nil
	}
	text := w.replies[w.i]
	w.i++
	return text, nil
}

func (w *seqWorker) ClearHistory()             {}
func (w *seqWorker) History() []brain.Turn     { return nil }
func (w *seqWorker) LoadHistory([]brain.Turn) {}

func TestJobEventsEmitOnStartAndDone(t *testing.T) {
	bus := events.NewBus()
	var mu sync.Mutex
	var kinds []string
	bus.SubscribeAll(func(e events.Event) {
		mu.Lock()
		kinds = append(kinds, events.EventType(e))
		mu.Unlock()
	})
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), kinds...)
	}

	worker := &fakeBrain{chunks: []string{"all set"}}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{Events: bus}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind:       RouteSpawn,
		Transcript: "status check",
		Source:     "test",
	})

	deadline := time.After(3 * time.Second)
	for {
		hasStart, hasFinish := false, false
		kinds := snapshot()
		for _, k := range kinds {
			if k == "job_started" {
				hasStart = true
			}
			if k == "job_finished" {
				hasFinish = true
			}
		}
		if hasStart && hasFinish {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("events = %v, want job_started and job_finished", kinds)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
