package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type quietTimingTrace struct {
	mu                                sync.Mutex
	pause, yielded, turnDone, invoked time.Time
}

// This controlled owner represents a quiet frame boundary. It uses the real
// coordinator's permit and the production pipeline's STT yield protocol;
// microphone/VAD recognition and audio-device startup are outside this metric.
type quietTimingSession struct {
	events      chan stt.Event
	bound       chan struct{}
	resumed     chan struct{}
	mu          sync.Mutex
	coordinator *work.Coordinator
	trace       *quietTimingTrace
}

var _ stt.CompletionCapture = (*quietTimingSession)(nil)

func (s *quietTimingSession) Events() <-chan stt.Event { return s.events }
func (*quietTimingSession) Close() error               { return nil } // no fixture goroutines or hardware
func (s *quietTimingSession) BindCompletionCoordinator(c *work.Coordinator) {
	s.mu.Lock()
	s.coordinator = c
	s.mu.Unlock()
	close(s.bound)
}
func (*quietTimingSession) HasCapturedInput() bool { return false }
func (s *quietTimingSession) PauseForCompletion(ctx context.Context, scope work.WorkScope, requestID string) (stt.PauseResult, error) {
	s.mu.Lock()
	c := s.coordinator
	s.mu.Unlock()
	if c == nil {
		return stt.PauseResult{}, errors.New("quiet capture has no bound coordinator")
	}
	permit, err := c.AcquireCapturePermit(scope, requestID, 1)
	if err != nil {
		return stt.PauseResult{}, err
	}
	token := stt.PauseToken{Scope: scope, RequestID: requestID, Epoch: 1, Permit: permit}
	s.trace.mu.Lock()
	s.trace.pause = time.Now()
	s.trace.yielded = time.Now()
	s.trace.mu.Unlock()
	select {
	case s.events <- stt.YieldedForCompletion{Scope: scope, RequestID: requestID, Epoch: token.Epoch, Token: token}:
		return stt.PauseResult{Decision: stt.PauseAcknowledged, Token: token}, nil
	case <-ctx.Done():
		_ = c.ReleaseCapturePermit(permit)
		return stt.PauseResult{}, ctx.Err()
	}
}
func (s *quietTimingSession) ResumeCapture(context.Context, stt.PauseToken) error {
	close(s.resumed)
	return nil
}

type quietTimingSTT struct{ session *quietTimingSession }

func (*quietTimingSTT) Available() bool { return true }
func (p *quietTimingSTT) Start(context.Context) (stt.Session, error) {
	p.session.events <- stt.PhaseEvent{Phase: "listening"}
	return p.session, nil
}

// Embedding preserves production PauseForCompletion, RunTurn, capture readiness,
// and pending checks. Only the actual background invocation is instrumented.
type quietTimingRunner struct {
	*pipeline.Pipeline
	trace    *quietTimingTrace
	finished chan error
}

func (r *quietTimingRunner) RunBackgroundTurn(ctx context.Context) error {
	r.trace.mu.Lock()
	r.trace.invoked = time.Now()
	r.trace.mu.Unlock()
	err := r.Pipeline.RunBackgroundTurn(ctx)
	r.finished <- err
	return err
}

type quietTimingTerminal struct {
	recoveryTerminal
	trace    *quietTimingTrace
	cmdMu    sync.Mutex
	stopping bool
	commands sync.WaitGroup
}

func (h *quietTimingTerminal) Init() tea.Cmd {
	return h.track(tea.Batch(h.model.resumeListening(), h.model.waitBackground(), h.model.backgroundRecoveryCmd()))
}
func (h *quietTimingTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if done, ok := msg.(voiceTurnDoneMsg); ok && done.err == nil && done.text == "" {
		h.trace.mu.Lock()
		h.trace.turnDone = time.Now()
		h.trace.mu.Unlock()
	}
	_, cmd := h.recoveryTerminal.Update(msg)
	return h, h.track(cmd)
}

// Bubble Tea joins its event loop but deliberately does not join Cmd workers.
// Keep cancellation-aware production commands observable during test teardown,
// including the ready waiter and recovery timer nested in Batch messages.
func (h *quietTimingTerminal) track(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		h.cmdMu.Lock()
		if h.stopping {
			h.cmdMu.Unlock()
			return nil
		}
		h.commands.Add(1)
		h.cmdMu.Unlock()
		defer h.commands.Done()
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for i := range batch {
				batch[i] = h.track(batch[i])
			}
		}
		return msg
	}
}

func (h *quietTimingTerminal) joinCommands() {
	h.cmdMu.Lock()
	h.stopping = true
	h.cmdMu.Unlock()
	h.commands.Wait()
}

func TestWorkQuietCompletionHostOverhead(t *testing.T) {
	samples := make([]time.Duration, 0, 30)
	for i := 0; i < 30; i++ {
		samples = append(samples, measureQuietCompletion(t, i))
	}
	p95 := nearestRankTiming(append([]time.Duration(nil), samples...), .95)
	t.Logf("canonical Publish while passive capture → Bubble Tea invocation of production RunBackgroundTurn; controlled quiet owner; raw=%v p50=%s p95=%s", samples, nearestRankTiming(append([]time.Duration(nil), samples...), .5), p95)
	if p95 > 300*time.Millisecond {
		t.Fatalf("quiet-completion host overhead p95=%s exceeds 300ms", p95)
	}
}

func measureQuietCompletion(t *testing.T, iteration int) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	trace := &quietTimingTrace{}
	session := &quietTimingSession{events: make(chan stt.Event, 2), bound: make(chan struct{}), resumed: make(chan struct{}), trace: trace}
	cfg := &config.Config{}
	b := &acceptanceHistoryBrain{}
	p := &pipeline.Pipeline{Brain: b, STT: &quietTimingSTT{session}, Events: events.NewBus()}
	p.Agency = pipeline.NewAgency(p, jobs, &stubBrain{}, cfg, nil)
	p.Agency.Wire()
	if err := p.EnableConversationWork(fmt.Sprintf("quiet-timing-%d", iteration), nil, cfg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.CloseWork(); err != nil {
			t.Errorf("close conversation work: %v", err)
		}
	}()
	listening := make(chan struct{}, 1)
	var inputs, yields atomic.Int32
	events.Subscribe(p.Events, func(e events.STTPhase) {
		if e.Phase == "listening" {
			select {
			case listening <- struct{}{}:
			default:
			}
		}
	})
	events.Subscribe(p.Events, func(events.UserInput) { inputs.Add(1) })
	events.Subscribe(p.Events, func(e events.TurnMetrics) {
		if e.Outcome == "yielded_for_completion" {
			yields.Add(1)
		}
	})
	runner := &quietTimingRunner{Pipeline: p, trace: trace, finished: make(chan error, 1)}
	var turns sync.WaitGroup
	m := sizedConversation(t, 100, 28)
	m.deps.ctx, m.deps.runner, m.deps.wg = ctx, runner, &turns
	m.deps.voice, m.voiceEnabled = true, true
	host := &quietTimingTerminal{recoveryTerminal: recoveryTerminal{model: m}, trace: trace}
	program := tea.NewProgram(host, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	defer func() {
		cancel()
		program.Quit()
		<-done
		host.joinCommands()
		turns.Wait()
	}()
	select {
	case <-session.bound:
	case <-ctx.Done():
		t.Fatal("production pipeline did not bind capture coordinator")
	}
	select {
	case <-listening:
	case <-ctx.Done():
		t.Fatal("production RunTurn did not consume listening phase")
	}
	c, scope := p.WorkCoordinator(), p.WorkSnapshot().Scope
	key, err := c.Register(scope, work.Admission{Task: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "Denver quiet fixture forecast"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runner.finished:
		if err != nil {
			t.Fatalf("production background turn: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("canonical result did not cross the real quiet-capture handoff")
	}
	// Let Bubble Tea dispatch its next capture command before shutdown. Its
	// WaitGroup counts commands when queued, and cancellation between queueing
	// and execution could otherwise leave a discarded command counted forever.
	select {
	case <-session.resumed:
	case <-ctx.Done():
		t.Fatal("host did not resume the yielded capture after completion")
	}
	trace.mu.Lock()
	pause, yielded, turnDone, invoked := trace.pause, trace.yielded, trace.turnDone, trace.invoked
	trace.mu.Unlock()
	if pause.IsZero() || yielded.IsZero() || turnDone.IsZero() || invoked.IsZero() || pause.Before(started) || yielded.Before(pause) || turnDone.Before(yielded) || invoked.Before(turnDone) {
		t.Fatalf("invalid real handoff order: publish=%s pause=%s yielded=%s hostVoiceDone=%s background=%s", started, pause, yielded, turnDone, invoked)
	}
	if inputs.Load() != 0 || yields.Load() != 1 {
		t.Fatalf("UserInput=%d yielded RunTurn metrics=%d; want 0 and 1", inputs.Load(), yields.Load())
	}
	snapshot := c.Snapshot(scope)
	// The timing ends at invocation. There is deliberately no audio fixture:
	// production must retain the voice delivery as held, never claim it spoken.
	if len(snapshot.Pending) != 1 || len(snapshot.Communicated) != 0 || snapshot.Pending[0].HoldReason != "audio_unavailable" || snapshot.Pending[0].LastAttempt == nil {
		t.Fatalf("expected real completion attempt held without audio: %+v", snapshot)
	}
	history := b.History()
	if len(history) != 1 || history[0].Role != "assistant" || !strings.Contains(history[0].Content, "Denver quiet fixture forecast") {
		t.Fatalf("production completion did not commit canonical history: %+v", history)
	}
	return invoked.Sub(started)
}
