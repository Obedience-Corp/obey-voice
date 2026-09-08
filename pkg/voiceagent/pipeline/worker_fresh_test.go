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

// forkingWorker hands out one child per job and remembers which child ran.
type forkingWorker struct {
	mu       sync.Mutex
	children []*forkedChild
	cleared  int
}

type forkedChild struct {
	mu    sync.Mutex
	calls int
}

func (w *forkingWorker) Fresh() brain.Provider {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := &forkedChild{}
	w.children = append(w.children, c)
	return c
}

func (w *forkingWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (w *forkingWorker) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (w *forkingWorker) ClearHistory() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cleared++
}
func (w *forkingWorker) History() []brain.Turn    { return nil }
func (w *forkingWorker) LoadHistory([]brain.Turn) {}

func (c *forkedChild) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (c *forkedChild) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return "done", nil
}
func (c *forkedChild) ClearHistory()            {}
func (c *forkedChild) History() []brain.Turn    { return nil }
func (c *forkedChild) LoadHistory([]brain.Turn) {}

// Every job runs on a conversation of its own: a provider that can fork is
// forked per job and the shared instance is never cleared.
func TestEachJobRunsOnAFreshWorker(t *testing.T) {
	fw := &forkingWorker{}
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	bus := events.NewBus()
	finished := make(chan events.JobFinished, 4)
	events.Subscribe(bus, func(e events.JobFinished) { finished <- e })
	p := &Pipeline{TTS: &progressiveFakeTTS{}, Player: player, Events: bus}
	a := NewAgency(p, job.NewTable(context.Background()), fw, &config.Config{}, nil)
	a.Wire()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)

	for _, task := range []string{"audit the config", "write the report"} {
		a.HandleRoute(context.Background(), turn, metrics, RouteDecision{Kind: RouteSpawn, Transcript: task, Source: "test"})
	}
	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d job(s) finished", i)
		}
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.children) != 2 || fw.cleared != 0 {
		t.Fatalf("forks = %d, clears = %d; want one fork per job and no clear", len(fw.children), fw.cleared)
	}
	for i, c := range fw.children {
		c.mu.Lock()
		calls := c.calls
		c.mu.Unlock()
		if calls != 1 {
			t.Fatalf("child %d ran %d times, want 1", i, calls)
		}
	}
}

// A provider that cannot fork is cleared before its job, but never while
// another job is still talking through it.
func TestUnforkableWorkerIsClearedOnlyWhenAlone(t *testing.T) {
	a := &Agency{jobs: job.NewTable(context.Background())}
	w := &forkingWorkerNoFork{}
	a.freshWorker(w)
	if w.cleared != 1 {
		t.Fatalf("cleared = %d, want 1 with no job running", w.cleared)
	}
}

type forkingWorkerNoFork struct{ cleared int }

func (w *forkingWorkerNoFork) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (w *forkingWorkerNoFork) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (w *forkingWorkerNoFork) ClearHistory()            { w.cleared++ }
func (w *forkingWorkerNoFork) History() []brain.Turn    { return nil }
func (w *forkingWorkerNoFork) LoadHistory([]brain.Turn) {}
