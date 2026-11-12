package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// These tests replay the 2026-11-12 session shape: one persona's background
// job finishes, reports its tools and becomes deliverable while another turn
// is still waiting on a slow model. The live persona and the job's persona
// share one pipeline here, as they do in the TUI; persona identity only
// changes who may receive the result on hosted brains.

const (
	jobTurnReply   = "Dylan seems tired today. Give him a minute before pushing real work."
	jobTurnRequest = "Obey, what do you think about Dylan?"
)

type gatedReplyBrain struct {
	*handoffCompletionBrain
	reply   string
	entered chan struct{}
	release chan struct{}
}

func (b *gatedReplyBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		select {
		case out <- b.reply:
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// numberedWorker blocks every job until release, then gives each job its own
// answer so deliveries can be told apart.
type numberedWorker struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (w *numberedWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, fmt.Errorf("unused")
}
func (w *numberedWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	n := w.calls.Add(1)
	w.started <- struct{}{}
	select {
	case <-w.release:
		return fmt.Sprintf("Weather report number %d is mild.", n), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (*numberedWorker) ClearHistory()            {}
func (*numberedWorker) History() []brain.Turn    { return nil }
func (*numberedWorker) LoadHistory([]brain.Turn) {}

type jobTurnRig struct {
	p      *Pipeline
	engine *stopGenerationEngine
	tts    *fakeTTS
	brain  *gatedReplyBrain
	worker *numberedWorker
	voice  bool

	mu      sync.Mutex
	ready   []events.ResponseReady
	metrics []events.TurnMetrics
	errs    []events.Error
}

func newJobTurnRig(t *testing.T, voice bool, playFor time.Duration) *jobTurnRig {
	t.Helper()
	jobs := job.NewTable(context.Background(), job.WithMaxConcurrent(2))
	t.Cleanup(jobs.Close)
	r := &jobTurnRig{
		engine: &stopGenerationEngine{&generationTestEngine{fakePlayer: newFakePlayer(playFor)}},
		tts:    &fakeTTS{delay: time.Millisecond},
		brain: &gatedReplyBrain{
			handoffCompletionBrain: &handoffCompletionBrain{lifecycleWorker: &lifecycleWorker{reply: "unused"}},
			reply:                  jobTurnReply,
			entered:                make(chan struct{}),
			release:                make(chan struct{}),
		},
		worker: &numberedWorker{started: make(chan struct{}, 4), release: make(chan struct{})},
		voice:  voice,
	}
	t.Cleanup(func() { r.engine.Close() })
	r.p = &Pipeline{Brain: r.brain, TTS: r.tts, Player: r.engine, Events: events.NewBus()}
	if voice {
		r.p.STT = &fakeSTT{text: jobTurnRequest}
	}
	cfg := &config.Config{VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker", ActivePersona: "dylan"}
	a := NewAgency(r.p, jobs, r.worker, cfg, nil)
	a.Wire()
	r.p.Agency = a
	if err := r.p.EnableConversationWork("job-during-turn", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.p.CloseWork() })
	// The host owns capture, as the TUI does once it has paused listening for
	// a delivery. Nothing else stands between a pending result and speech.
	r.p.externalCompletionCapture = func(_ context.Context, c *work.Coordinator, id string) (work.CapturePermit, error) {
		return c.AcquireCapturePermit(c.Scope(), id, r.p.captureRequests.Load())
	}
	events.Subscribe(r.p.Events, func(e events.ResponseReady) { r.mu.Lock(); r.ready = append(r.ready, e); r.mu.Unlock() })
	events.Subscribe(r.p.Events, func(e events.TurnMetrics) { r.mu.Lock(); r.metrics = append(r.metrics, e); r.mu.Unlock() })
	events.Subscribe(r.p.Events, func(e events.Error) { r.mu.Lock(); r.errs = append(r.errs, e); r.mu.Unlock() })
	return r
}

func (r *jobTurnRig) spawn(t *testing.T, task string) string {
	t.Helper()
	before := len(r.p.WorkSnapshot().Jobs)
	metrics := newTurnMetrics()
	r.p.Agency.HandleRoute(context.Background(), r.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: task, Source: "test"})
	select {
	case <-r.worker.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	jobs := r.p.WorkSnapshot().Jobs
	if len(jobs) != before+1 {
		t.Fatalf("jobs after spawn = %d, want %d", len(jobs), before+1)
	}
	return jobs[len(jobs)-1].Key.JobID
}

func (r *jobTurnRig) startTurn(t *testing.T) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		if r.voice {
			_, err := r.p.RunTurn(context.Background())
			done <- err
			return
		}
		done <- r.p.RunTurnTextMode(context.Background(), jobTurnRequest)
	}()
	select {
	case <-r.brain.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("turn never reached the model")
	}
	return done
}

// reportTools emits what a worker's tool loop emits while it runs.
func (r *jobTurnRig) reportTools(jobID string, n int) {
	start, end := r.p.jobToolHooks(jobID, r.p.WorkSnapshot().Scope)
	for i := range n {
		id := fmt.Sprintf("%s-tool-%d", jobID, i)
		call := brain.ToolCall{ID: id, Name: "web_fetch", Summary: "weather"}
		start(call)
		end(brain.ToolResult{ToolCall: call, Preview: "mid forties"})
	}
}

func (r *jobTurnRig) finishJobs(t *testing.T, n int) {
	t.Helper()
	close(r.worker.release)
	waitWorkPending(t, r.p.WorkCoordinator(), r.p.WorkSnapshot().Scope, n)
}

func (r *jobTurnRig) awaitTurn(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	}
}

// assertReplySpoken checks the turn finished uninterrupted with every reply
// sentence synthesized and admitted to the player.
func (r *jobTurnRig) assertReplySpoken(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	ready := slices.Clone(r.ready)
	metrics := slices.Clone(r.metrics)
	errs := slices.Clone(r.errs)
	r.mu.Unlock()
	if len(ready) == 0 || ready[len(ready)-1].Response != jobTurnReply || ready[len(ready)-1].Interrupted {
		t.Fatalf("turn response = %+v, want the reply uninterrupted", ready)
	}
	var last events.TurnMetrics
	for _, m := range metrics {
		if m.Outcome != "" {
			last = m
		}
	}
	if last.Outcome != "completed" || last.Interrupted || last.SpeechStop != nil {
		t.Fatalf("turn metrics outcome=%q interrupted=%v stop=%+v", last.Outcome, last.Interrupted, last.SpeechStop)
	}
	if len(errs) != 0 {
		t.Fatalf("turn reported errors: %+v", errs)
	}
	texts := r.tts.Texts()
	spoken := strings.Join(texts, " ")
	if !strings.Contains(spoken, "Dylan seems tired today.") || !strings.Contains(spoken, "Give him a minute") {
		t.Fatalf("reply was not synthesized: %q", texts)
	}
	if strings.Contains(spoken, "Weather report number") {
		t.Fatalf("a job result was spoken inside the live turn: %q", texts)
	}
	r.engine.mu.Lock()
	accepted := r.engine.accepted
	r.engine.mu.Unlock()
	if accepted != len(texts) {
		t.Fatalf("player admitted %d of %d synthesized segments: %q", accepted, len(texts), texts)
	}
}

// deliverAfter runs the idle delivery and checks every result is spoken after
// the reply's last sentence. Delivery receipts need a player that reports
// audible writes, which the generation fake does not.
func (r *jobTurnRig) deliverAfter(t *testing.T, results int) {
	t.Helper()
	r.p.Player = &fencedHandoffPlayer{}
	if err := r.p.RunBackgroundTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	texts := r.tts.Texts()
	reply := slices.IndexFunc(texts, func(s string) bool { return strings.Contains(s, "Give him a minute") })
	var delivered []int
	for i, s := range texts {
		if strings.Contains(s, "Weather report number") {
			delivered = append(delivered, i)
		}
	}
	if reply < 0 || len(delivered) != results {
		t.Fatalf("spoken = %q, want the reply and %d results", texts, results)
	}
	for _, i := range delivered {
		if i < reply {
			t.Fatalf("result spoken before the reply finished: %q", texts)
		}
	}
	if pending := r.p.WorkCoordinator().Pending(r.p.WorkSnapshot().Scope); len(pending) != 0 {
		t.Fatalf("results still pending after delivery: %+v", pending)
	}
}

func TestJobFinishingWhileAnotherTurnThinksLeavesItsReplyAudible(t *testing.T) {
	for _, voice := range []bool{true, false} {
		t.Run(map[bool]string{true: "voice", false: "text with voice output"}[voice], func(t *testing.T) {
			r := newJobTurnRig(t, voice, time.Millisecond)
			jobID := r.spawn(t, "weather in Denver")
			done := r.startTurn(t)

			r.reportTools(jobID, 2)
			r.finishJobs(t, 1)
			close(r.brain.release)
			r.awaitTurn(t, done)
			r.assertReplySpoken(t)
			// The pipeline never delivers on its own: the result waits, unclaimed,
			// for the host's idle boundary (the TUI gates it on turnIdle).
			if pending := r.p.WorkCoordinator().Pending(r.p.WorkSnapshot().Scope); len(pending) != 1 || pending[0].State != work.Pending {
				t.Fatalf("result lost or claimed by the live turn: %+v", pending)
			}
			r.deliverAfter(t, 1)
		})
	}
}

func TestJobFinishingMidSpeechDoesNotCutTheReply(t *testing.T) {
	r := newJobTurnRig(t, true, time.Millisecond)
	jobID := r.spawn(t, "weather in Denver")
	done := r.startTurn(t)
	// Let the acknowledgement finish so the next start is the reply's.
	deadline := time.Now().Add(3 * time.Second)
	for r.engine.IsPlaying() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for len(r.engine.StartedSignal()) > 0 {
		<-r.engine.StartedSignal()
	}
	releaseReply := r.engine.HoldPlayback()
	defer releaseReply()
	close(r.brain.release)
	select {
	case <-r.engine.StartedSignal():
	case <-time.After(3 * time.Second):
		t.Fatal("reply never started playing")
	}
	stopsWhilePlaying := r.engine.StopCount()
	r.reportTools(jobID, 2)
	r.finishJobs(t, 1)
	if !r.engine.IsPlaying() {
		t.Fatal("the reply finished before the job did, so this run did not cover mid-speech")
	}
	releaseReply()
	r.awaitTurn(t, done)
	if got := r.engine.StopCount(); got != stopsWhilePlaying {
		t.Fatalf("player stopped %d times while the reply played", got-stopsWhilePlaying)
	}
	r.assertReplySpoken(t)
	r.deliverAfter(t, 1)
}

func TestTwoJobsFinishingDuringOneTurnAreBothDeliveredAfterIt(t *testing.T) {
	r := newJobTurnRig(t, true, time.Millisecond)
	first := r.spawn(t, "weather in Denver")
	second := r.spawn(t, "weather in Boulder")
	done := r.startTurn(t)
	r.reportTools(first, 1)
	r.reportTools(second, 1)
	r.finishJobs(t, 2)
	close(r.brain.release)
	r.awaitTurn(t, done)
	r.assertReplySpoken(t)
	r.deliverAfter(t, 2)
}
