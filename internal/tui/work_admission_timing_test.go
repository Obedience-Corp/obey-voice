package tui

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	tea "github.com/charmbracelet/bubbletea"
)

type admissionTimingWorker struct {
	stubBrain
	started chan struct{}
}

func (w *admissionTimingWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	close(w.started)
	<-ctx.Done()
	return "", ctx.Err()
}

type admissionTimingSession struct {
	events chan stt.Event
	once   sync.Once
}

func (s *admissionTimingSession) Events() <-chan stt.Event { return s.events }
func (s *admissionTimingSession) Close() error {
	s.once.Do(func() { close(s.events) })
	return nil
}

type admissionTimingSTT struct {
	starts atomic.Int32
	next   chan time.Time
}

func (*admissionTimingSTT) Available() bool { return true }
func (s *admissionTimingSTT) Start(context.Context) (stt.Session, error) {
	session := &admissionTimingSession{events: make(chan stt.Event, 2)}
	session.events <- stt.PhaseEvent{Phase: "listening"}
	if s.starts.Add(1) == 1 {
		// Drive the real pipeline's deterministic work routing from STT.
		session.events <- stt.FinalTranscript{Text: "Look up the weather in Denver"}
	} else {
		select {
		case s.next <- time.Now():
		default:
		}
	}
	return session, nil
}

type admissionTimingTerminal struct{ recoveryTerminal }

func (h *admissionTimingTerminal) Init() tea.Cmd { return h.model.resumeListening() }
func (h *admissionTimingTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := h.recoveryTerminal.Update(msg)
	return h, cmd
}

// Measure host/pipeline overhead with controlled STT and no synthesis delay.
// The actual first RunTurn admits work through Agency, then Bubble Tea must
// start the next production RunTurn while that worker remains blocked. This
// does not claim microphone startup or spoken acknowledgement takes <100ms.
func TestWorkAdmissionToNextCaptureHostOverhead(t *testing.T) {
	var samples []time.Duration
	for i := 0; i < 30; i++ {
		samples = append(samples, measureAdmissionCapture(t, i))
	}
	p95 := nearestRankTiming(append([]time.Duration(nil), samples...), .95)
	t.Logf("accepted Agency route → next production STT.Start, blocked worker; raw=%v p50=%s p95=%s", samples, nearestRankTiming(append([]time.Duration(nil), samples...), .5), p95)
	if p95 > 100*time.Millisecond {
		t.Fatalf("admission-to-capture host overhead p95=%s exceeds 100ms", p95)
	}
}

func measureAdmissionCapture(t *testing.T, iteration int) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	worker := &admissionTimingWorker{started: make(chan struct{})}
	capture := &admissionTimingSTT{next: make(chan time.Time, 1)}
	cfg := &config.Config{BrainProvider: "ollama", OllamaModel: "fixture", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture", VoiceWorkEnabled: true}
	p := &pipeline.Pipeline{Brain: &acceptanceHistoryBrain{}, STT: capture, Events: events.NewBus()}
	p.Agency = pipeline.NewAgency(p, jobs, worker, cfg, nil)
	p.Agency.Wire()
	if err := p.EnableConversationWork(fmt.Sprintf("admission-timing-%d", iteration), nil, cfg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.CloseWork(); err != nil {
			t.Errorf("close conversation work: %v", err)
		}
	}()
	accepted := make(chan time.Time, 1)
	events.Subscribe(p.Events, func(e events.RouteDecided) {
		if e.State == "accepted" {
			select {
			case accepted <- time.Now():
			default:
			}
		}
	})
	var turns sync.WaitGroup
	m := sizedConversation(t, 100, 28)
	m.deps.ctx, m.deps.runner, m.deps.wg = ctx, p, &turns
	m.deps.voice, m.voiceEnabled = true, true
	h := &admissionTimingTerminal{recoveryTerminal: recoveryTerminal{model: m}}
	program := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	defer func() {
		cancel()
		program.Quit()
		<-done
		turns.Wait()
	}()
	var admitted, available time.Time
	select {
	case admitted = <-accepted:
	case <-ctx.Done():
		t.Fatal("production Agency never accepted work")
	}
	select {
	case available = <-capture.next:
	case <-ctx.Done():
		t.Fatal("next capture depended on blocked worker completion")
	}
	select {
	case <-worker.started:
	case <-ctx.Done():
		t.Fatal("worker never executed")
	}
	snapshot := p.WorkSnapshot()
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != work.JobRunning || len(snapshot.Pending) != 0 {
		t.Fatalf("worker must still run at next capture: %+v", snapshot)
	}
	if available.Before(admitted) {
		t.Fatal("next capture preceded work admission")
	}
	return available.Sub(admitted)
}
