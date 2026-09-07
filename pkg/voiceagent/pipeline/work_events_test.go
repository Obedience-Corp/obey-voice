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

// collectToolEvents subscribes to the tool events a pipeline emits.
func collectToolEvents(bus *events.Bus, started *[]events.ToolCallStarted, finished *[]events.ToolCallFinished) {
	events.Subscribe(bus, func(e events.ToolCallStarted) { *started = append(*started, e) })
	events.Subscribe(bus, func(e events.ToolCallFinished) { *finished = append(*finished, e) })
}

func TestJobToolHooksAttributeToTheJob(t *testing.T) {
	bus := events.NewBus()
	var started []events.ToolCallStarted
	var finished []events.ToolCallFinished
	collectToolEvents(bus, &started, &finished)

	bumps := 0
	p := &Pipeline{Events: bus}
	p.brainDeadlineBump = func() { bumps++ }

	onStart, onEnd := p.jobToolHooks("job-7")
	call := brain.ToolCall{ID: "call_3", Name: "run_command", Kind: brain.KindExecute, Summary: "ls"}
	onStart(call)
	onEnd(brain.ToolResult{ToolCall: call, Preview: "a\nb", Err: "error: boom", Elapsed: 250 * time.Millisecond})

	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("emitted %d start / %d finish events, want one each", len(started), len(finished))
	}
	if started[0].JobID != "job-7" || finished[0].JobID != "job-7" {
		t.Errorf("job ids = %q / %q, want job-7", started[0].JobID, finished[0].JobID)
	}
	if started[0].Kind != string(brain.KindExecute) || finished[0].Kind != string(brain.KindExecute) {
		t.Errorf("kinds = %q / %q, want execute", started[0].Kind, finished[0].Kind)
	}
	if started[0].ID != "call_3" || finished[0].ID != "call_3" {
		t.Errorf("call ids = %q / %q, want call_3", started[0].ID, finished[0].ID)
	}
	if finished[0].Err != "error: boom" || finished[0].Elapsed != 250*time.Millisecond {
		t.Errorf("finish = %+v, want the error and elapsed carried through", finished[0])
	}
	if bumps != 0 {
		t.Errorf("brain deadline bumped %d times, want 0: worker tools must not extend the live turn watchdog", bumps)
	}
}

func TestLiveToolHooksKeepEmptyJobIDAndBump(t *testing.T) {
	bus := events.NewBus()
	var started []events.ToolCallStarted
	var finished []events.ToolCallFinished
	collectToolEvents(bus, &started, &finished)

	bumps := 0
	p := &Pipeline{Events: bus}
	p.brainDeadlineBump = func() { bumps++ }

	call := brain.ToolCall{ID: "route#1", Name: "route", Kind: brain.KindRoute, Summary: "spawn"}
	p.toolStartHook()(call)
	p.toolEndHook()(brain.ToolResult{ToolCall: call, Preview: "spawn"})

	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("emitted %d start / %d finish events, want one each", len(started), len(finished))
	}
	if started[0].JobID != "" || finished[0].JobID != "" {
		t.Errorf("job ids = %q / %q, want empty for the live brain", started[0].JobID, finished[0].JobID)
	}
	if bumps != 2 {
		t.Errorf("brain deadline bumped %d times, want 2", bumps)
	}
}

// immediateHookWorker fires its tool hooks and returns before Table.Start can
// return: the fastest worker there is.
type immediateHookWorker struct{}

func (immediateHookWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{"done"}}).ThinkStream(ctx, input, opts)
}

func (immediateHookWorker) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	call := brain.ToolCall{ID: "call_1", Name: "list_files", Kind: brain.KindListDir, Summary: "."}
	if opts.OnToolStart != nil {
		opts.OnToolStart(call)
	}
	if opts.OnToolEnd != nil {
		opts.OnToolEnd(brain.ToolResult{ToolCall: call, Preview: "README.md"})
	}
	return "done", nil
}

func (immediateHookWorker) ClearHistory()            {}
func (immediateHookWorker) History() []brain.Turn    { return nil }
func (immediateHookWorker) LoadHistory([]brain.Turn) {}

func TestJobStartedPrecedesWorkerEvents(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	bus := events.NewBus()
	var mu sync.Mutex
	var seen []events.Event
	finished := make(chan struct{}, 1)
	bus.SubscribeAll(func(e events.Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
		if _, ok := e.(events.JobFinished); ok {
			finished <- struct{}{}
		}
	})

	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: bus}
	agency := NewAgency(p, jobs, immediateHookWorker{}, &config.Config{}, nil)
	agency.Wire()

	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(context.Background(), turn, metrics, RouteDecision{
		Kind: RouteSpawn, Transcript: "list the files here", Source: "test",
	})
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	started, tool, done := -1, -1, -1
	jobID := ""
	for i, e := range seen {
		switch ev := e.(type) {
		case events.JobStarted:
			if started < 0 {
				started, jobID = i, ev.ID
			}
		case events.ToolCallStarted:
			if ev.JobID != "" && tool < 0 {
				tool = i
				if ev.JobID != jobID {
					t.Errorf("tool event job id = %q, want %q", ev.JobID, jobID)
				}
			}
		case events.JobFinished:
			done = i
		}
	}
	if started < 0 || tool < 0 || done < 0 {
		t.Fatalf("missing events: started=%d tool=%d finished=%d", started, tool, done)
	}
	if !(started < tool && tool < done) {
		t.Fatalf("order started=%d tool=%d finished=%d: JobStarted must precede every worker event", started, tool, done)
	}
}
