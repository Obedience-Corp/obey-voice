package pipeline

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// toolWorker runs scripted tool calls through the stream hooks before it
// returns, so a spawned job produces the fold rows the Agency reads back.
type toolWorker struct {
	calls   []brain.ToolCall
	ran     chan struct{}
	release chan struct{}
	reply   string
	once    sync.Once
}

func (w *toolWorker) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	return (&fakeBrain{chunks: []string{w.reply}}).ThinkStream(ctx, input, opts)
}

func (w *toolWorker) ThinkFull(ctx context.Context, _ string, opts brain.StreamOptions) (string, error) {
	for _, call := range w.calls {
		if opts.OnToolStart != nil {
			opts.OnToolStart(call)
		}
		if opts.OnToolEnd != nil {
			opts.OnToolEnd(brain.ToolResult{ToolCall: call, Elapsed: 10 * time.Millisecond})
		}
	}
	w.once.Do(func() { close(w.ran) })
	if w.release != nil {
		select {
		case <-w.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return w.reply, nil
}

func (w *toolWorker) ClearHistory()            {}
func (w *toolWorker) History() []brain.Turn    { return nil }
func (w *toolWorker) LoadHistory([]brain.Turn) {}

func TestJobStatusSpeaksTheFoldSummary(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	worker := &toolWorker{
		calls: []brain.ToolCall{
			{ID: "c1", Name: "read_file", Kind: brain.KindRead},
			{ID: "c2", Name: "read_file", Kind: brain.KindRead},
			{ID: "c3", Name: "run_command", Kind: brain.KindExecute},
		},
		ran:     make(chan struct{}),
		release: make(chan struct{}),
		reply:   "all done",
	}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()
	defer close(worker.release)

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind: RouteSpawn, Transcript: "audit the config", Source: "test",
	})

	select {
	case <-worker.ran:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never ran its tools")
	}

	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind: RouteJobStatus, Transcript: "what's happening", Source: "test",
	})

	spoken := strings.Join(provider.Calls(), " | ")
	if !strings.Contains(spoken, "Still working on audit the config.") {
		t.Fatalf("spoken = %q, want the task", spoken)
	}
	if !strings.Contains(spoken, "So far: read 2 files, ran 1 command.") {
		t.Fatalf("spoken = %q, want the folded summary", spoken)
	}
}

func TestJobStatusWithoutToolsKeepsTheBareLine(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()

	worker := &toolWorker{ran: make(chan struct{}), release: make(chan struct{}), reply: "done"}
	jobs := job.NewTable(context.Background())
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}
	agency := NewAgency(p, jobs, worker, &config.Config{}, nil)
	agency.Wire()
	defer close(worker.release)

	ctx := context.Background()
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind: RouteSpawn, Transcript: "audit the config", Source: "test",
	})
	select {
	case <-worker.ran:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never started")
	}

	agency.HandleRoute(ctx, turn, metrics, RouteDecision{
		Kind: RouteJobStatus, Transcript: "status", Source: "test",
	})

	spoken := strings.Join(provider.Calls(), " | ")
	if !strings.Contains(spoken, "Still working on audit the config.") {
		t.Fatalf("spoken = %q, want the bare status line", spoken)
	}
	if strings.Contains(spoken, "So far:") {
		t.Fatalf("spoken = %q, want no fold clause before any tool ran", spoken)
	}
}

func TestWorkNarrationSpeaksOncePerClosedRow(t *testing.T) {
	cases := []struct {
		name      string
		narration string
		// midJob is what has been enqueued once the read row closed and the
		// execute row is still open; want adds what the job's end closes.
		midJob []string
		want   []string
	}{
		{name: "status_only is the silent default", narration: ""},
		{name: "status_only says nothing", narration: "status_only"},
		{
			name:      "milestones speaks one line per closed row",
			narration: "milestones",
			midJob:    []string{"Read 2 files."},
			want:      []string{"Read 2 files.", "Ran 1 command."},
		},
	}

	calls := []brain.ToolCall{
		{ID: "c1", Name: "read_file", Kind: brain.KindRead},
		{ID: "c2", Name: "read_file", Kind: brain.KindRead},
		{ID: "c3", Name: "run_command", Kind: brain.KindExecute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{Events: events.NewBus()}
			jobs := job.NewTable(context.Background())
			agency := NewAgency(p, jobs, nil, &config.Config{VoiceWorkNarration: tc.narration}, nil)
			agency.Wire()
			// A foreground turn holds the queue, so what was enqueued is
			// still there to count.
			p.incActiveTurn()

			start, end := agency.foldingToolHooks("job-1")
			for _, call := range calls {
				start(call)
				end(brain.ToolResult{ToolCall: call, Elapsed: 5 * time.Millisecond})
			}
			if got := pendingTexts(agency); !slices.Equal(got, tc.midJob) {
				t.Fatalf("mid-job pending = %v, want %v: only the closed row speaks", got, tc.midJob)
			}
			agency.closeFold("job-1", true)

			if got := pendingTexts(agency); !slices.Equal(got, tc.want) {
				t.Fatalf("pending = %v, want %v", got, tc.want)
			}
		})
	}
}

// Cancelling is silent even under milestones: the user who said "cancel that"
// did not ask to be told what the worker got through first.
func TestAbortSpeaksNothingUnderMilestones(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	jobs := job.NewTable(context.Background())
	agency := NewAgency(p, jobs, nil, &config.Config{VoiceWorkNarration: "milestones"}, nil)
	agency.Wire()
	p.incActiveTurn()

	start, end := agency.foldingToolHooks("job-1")
	call := brain.ToolCall{ID: "c1", Name: "run_command", Kind: brain.KindExecute}
	start(call)
	end(brain.ToolResult{ToolCall: call})

	agency.onJobDone("job-1", job.StateAborted, "", nil)

	if got := pendingTexts(agency); len(got) != 0 {
		t.Fatalf("pending = %v, want nothing spoken for an aborted job", got)
	}
}

func pendingTexts(a *Agency) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	texts := make([]string, 0, len(a.pending))
	for _, d := range a.pending {
		texts = append(texts, d.Text)
	}
	return texts
}
