package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	transcripts []string
	starts      atomic.Int32
	next        chan time.Time
}

func (*admissionTimingSTT) Available() bool { return true }
func (s *admissionTimingSTT) Start(context.Context) (stt.Session, error) {
	session := &admissionTimingSession{events: make(chan stt.Event, 2)}
	session.events <- stt.PhaseEvent{Phase: "listening"}
	if index := int(s.starts.Add(1)) - 1; index < len(s.transcripts) {
		session.events <- stt.FinalTranscript{Text: s.transcripts[index]}
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
	cfg := &config.Config{BrainProvider: "ollama", OllamaModel: "fixture", VoiceWorkerProvider: "ollama", VoiceWorkerModel: "fixture", VoiceWorkEnabled: true}
	p := &pipeline.Pipeline{Brain: &acceptanceHistoryBrain{}, Events: events.NewBus()}
	return measureCaptureDuringWork(t, ctx, p, cfg, fmt.Sprintf("admission-timing-%d", iteration), []string{"Look up the weather in Denver"})
}

// Exercise direct routing with an installed model. Hold the worker open so the
// second utterance can only complete if the first released the conversation.
func TestLiveDirectWorkerKeepsConversationOpen(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_WORKER_ROUTING_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_WORKER_ROUTING_MODEL to an installed Ollama model")
	}
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	cfg := &config.Config{
		BrainProvider: "ollama", OllamaHost: "http://localhost:11434", OllamaModel: model, OllamaNumCtx: 32768,
		VoiceWorkerProvider: "ollama", VoiceWorkerModel: model, VoiceWorkEnabled: true, VoiceToolsEnabled: true,
		AgentName: "Obey", WorkDir: root, WorkerBoundary: root, PromptsDir: filepath.Join(root, "prompts"), SkillsDir: filepath.Join(root, "skills"),
	}
	live, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: live, Events: events.NewBus(), VoiceToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsDirect}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	measureCaptureDuringWork(t, ctx, p, cfg, "live-handoff", []string{
		"Can you just get a new photo from the internet of a dog and not a cat and show me that?",
		"What is two plus two?",
	})
	history := live.History()
	if len(history) == 0 || history[len(history)-1].Role != "assistant" {
		t.Fatalf("missing second reply: %+v", history)
	}
	reply := history[len(history)-1].Content
	if !strings.Contains(strings.ToLower(reply), "four") && !strings.Contains(reply, "4") {
		t.Fatalf("unrelated question went unanswered: %q", reply)
	}
	t.Logf("foreground reply while worker was running: %q", reply)
}

func measureCaptureDuringWork(t *testing.T, parent context.Context, p *pipeline.Pipeline, cfg *config.Config, session string, inputs []string) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	worker := &admissionTimingWorker{started: make(chan struct{})}
	capture := &admissionTimingSTT{transcripts: inputs, next: make(chan time.Time, 1)}
	p.STT = capture
	pipeline.NewAgency(p, jobs, worker, cfg, nil).Wire()
	if err := p.EnableConversationWork(session, nil, cfg); err != nil {
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
