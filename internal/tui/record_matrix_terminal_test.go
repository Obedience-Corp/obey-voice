package tui

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
)

// Controlled providers exercise the actual App/pipeline/recorder owner. They
// never construct a native player/model/device and never claim real voice.
type matrixSTT struct{ inputs <-chan string }
type matrixSTTSession struct {
	cancel context.CancelFunc
	done   chan struct{}
	events chan stt.Event
}

func (*matrixSTT) Available() bool { return true }
func (p *matrixSTT) Start(ctx context.Context) (stt.Session, error) {
	child, cancel := context.WithCancel(ctx)
	s := &matrixSTTSession{cancel: cancel, done: make(chan struct{}), events: make(chan stt.Event, 2)}
	go func() {
		defer close(s.done)
		defer close(s.events)
		select {
		case <-child.Done():
			return
		case text := <-p.inputs:
			s.events <- stt.FinalTranscript{Text: text}
		}
	}()
	return s, nil
}
func (s *matrixSTTSession) Events() <-chan stt.Event { return s.events }
func (s *matrixSTTSession) Close() error             { s.cancel(); <-s.done; return nil }

type matrixBrain struct{ acceptanceHistoryBrain }

func (b *matrixBrain) AppendAssistantResponseWithIdentity(text string, _ brain.TurnIdentity) error {
	return b.AppendAssistantResponse(text)
}

func (b *matrixBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	chunks, done := make(chan string), make(chan brain.StreamResult, 1)
	go func() {
		defer close(chunks)
		defer close(done)
		if strings.Contains(input, "delayed") {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case <-time.After(350 * time.Millisecond):
			}
		}
		response := "Controlled " + input + " response."
		if len(opts.WorkResults) > 0 {
			response = "Controlled worker result: " + opts.WorkResults[0].Brief
		}
		select {
		case chunks <- response:
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
			return
		}
		done <- brain.StreamResult{}
	}()
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func (*matrixBrain) ThinkFull(ctx context.Context, _ string, opts brain.StreamOptions) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(40 * time.Millisecond):
	}
	if opts.OnWorkerUsage != nil {
		tokens := 4
		opts.OnWorkerUsage(brain.WorkerUsage{OutputTokens: &tokens})
	}
	return "Synthetic worker result completed.", nil
}

type matrixTTS struct{ primary bool }

func (*matrixTTS) Available() bool                       { return true }
func (*matrixTTS) ListVoices(string, string) []tts.Voice { return nil }
func (p *matrixTTS) ProviderName() string {
	if p.primary {
		return "synthetic-primary"
	}
	return "synthetic-fallback"
}
func (*matrixTTS) ModelTier() string     { return "fixture" }
func (*matrixTTS) SpeechPlace() string   { return "synthetic" }
func (*matrixTTS) WorkerStreaming() bool { return false }
func (p *matrixTTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	if p.primary && strings.Contains(text, "fallback") {
		return nil, &tts.ProviderError{Provider: "synthetic-primary", Kind: tts.ProviderErrorWorker, Err: errors.New("controlled primary synthesis failure")}
	}
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(audio.SampleRate); err != nil {
		return nil, err
	}
	if err := stream.Write(make([]float32, 1600)); err != nil {
		stream.Close()
		return nil, err
	}
	stream.Close()
	return stream, nil
}

type matrixPlayer struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	playing atomic.Bool
}

func (*matrixPlayer) SinkKind() string  { return audio.SinkStreamClient } // simulated stream; no listener/ear
func (p *matrixPlayer) IsPlaying() bool { return p.playing.Load() }
func (p *matrixPlayer) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (p *matrixPlayer) Close() error { p.Stop(); return nil }
func (p *matrixPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	child, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.playing.Store(true)
	started, done := make(chan struct{}), make(chan audio.PlaybackResult, 1)
	go func() {
		defer cancel()
		defer p.playing.Store(false)
		defer close(done)
		samples := 0
		began := false
		for {
			select {
			case <-child.Done():
				done <- audio.PlaybackResult{Interrupted: true, Err: child.Err()}
				return
			case frame, ok := <-stream.Frames():
				if !ok {
					goto drained
				}
				samples += len(frame)
				if !began {
					close(started)
					began = true
				}
			}
		}
	drained:
		select {
		case <-child.Done():
			done <- audio.PlaybackResult{Interrupted: true, Err: child.Err()}
		case <-time.After(400 * time.Millisecond):
			done <- audio.PlaybackResult{AudioSeconds: float64(samples) / audio.SampleRate, SynthesisEndedAt: stream.ClosedAt()}
		}
	}()
	return audio.NewPlayback(started, done), nil
}

type matrixTick struct{}
type matrixInterrupt struct{}
type matrixTerminal struct {
	app                        App
	inputs                     chan string
	cases                      []string
	index                      int
	active, interruptScheduled bool
	doneAt                     time.Time
}

func (m matrixTerminal) Init() tea.Cmd { return tea.Batch(m.app.Init(), matrixTickCmd()) }
func (m matrixTerminal) View() string  { return m.app.View() }
func matrixTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return matrixTick{} })
}
func (m matrixTerminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(matrixTick); ok {
		commands := []tea.Cmd{matrixTickCmd()}
		if m.app.runtime == nil {
			return m, tea.Batch(commands...)
		}
		conv := &m.app.conversation
		if m.index >= len(m.cases) {
			if conv.capture != nil {
				conv.configureRecord([]string{"off"})
			}
			if conv.capturePending != nil {
				return m, tea.Batch(append(commands, conv.capturePollCmd())...)
			}
			next, cmd := m.app.Update(quitMsg{})
			m.app = next.(App)
			return m, cmd
		}
		if conv.capture == nil && conv.capturePending == nil {
			conv.configureRecord([]string{"talk"})
			commands = append(commands, conv.capturePollCmd())
		}
		if !m.active && conv.turnState == turnIdle && !conv.voiceEnabled {
			next, cmd := m.app.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
			m.app = next.(App)
			return m, tea.Batch(append(commands, cmd)...)
		}
		if !m.active && conv.turnState == turnVoiceListening && conv.capture != nil && time.Since(m.doneAt) > 120*time.Millisecond {
			select {
			case m.inputs <- m.cases[m.index]:
				m.active = true
				m.interruptScheduled = false
				conv.commandNotice("SYNTHETIC owner matrix: " + m.cases[m.index])
			default:
			}
		}
		return m, tea.Batch(commands...)
	}
	if _, ok := msg.(matrixInterrupt); ok {
		m.app.conversation.input.SetValue("controlled followup after interruption")
		next, cmd := m.app.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.app = next.(App)
		return m, cmd
	}
	var interrupt tea.Cmd
	if message, ok := msg.(busEventMsg); ok && m.active && strings.Contains(m.cases[m.index], "interrupted") && !m.interruptScheduled {
		if _, speaking := message.event.(events.SpeakingStarted); speaking {
			m.interruptScheduled = true
			interrupt = tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return matrixInterrupt{} })
		}
	}
	if _, ok := msg.(voiceTurnDoneMsg); ok && m.active {
		m.index++
		m.active = false
		m.doneAt = time.Now()
	}
	next, cmd := m.app.Update(msg)
	m.app = next.(App)
	return m, tea.Batch(cmd, interrupt)
}
func TestRecordOwnerMatrixTerminalHarness(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_CAPTURE_MATRIX_ROOT")
	if root == "" {
		t.Skip("opt-in controlled owner terminal matrix")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("explicit absolute matrix root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	config.SetConfigDirForTest(t, root)
	cfg := &config.Config{AgentName: "Synthetic fixture", BrainProvider: "synthetic", TTSProvider: "synthetic", STTProvider: "synthetic", VoiceWorkEnabled: os.Getenv("OBEY_VOICE_CAPTURE_MATRIX_WORK") == "on"}
	inputs := make(chan string, 1)
	build := func(ctx context.Context, _ BuildProgress, _, _ string) (*ConversationRuntime, error) {
		bus := events.NewBus()
		player := &matrixPlayer{}
		pipe := &pipeline.Pipeline{STT: &matrixSTT{inputs: inputs}, Brain: &matrixBrain{}, TTS: &matrixTTS{primary: true}, TTSFallback: &matrixTTS{}, Player: player, Events: bus}
		jobs := job.NewTable(ctx)
		agency := pipeline.NewAgency(pipe, jobs, &matrixBrain{}, cfg, nil)
		agency.Wire()
		const sessionID = "controlled-owner-matrix"
		if cfg.VoiceWorkEnabled {
			if err := pipe.EnableConversationWork(sessionID, nil, cfg); err != nil {
				return nil, err
			}
		} else if err := pipe.BeginConversationIdentity(sessionID); err != nil {
			return nil, err
		}
		pipe.AdmitInput = func(_ context.Context, input string, _ bool) (pipeline.ResponseAdmission, error) {
			if strings.Contains(input, "rejected") {
				return pipeline.ResponseAdmission{}, errors.New("controlled rejected admission")
			}
			personaID := "fixture-a"
			if strings.Contains(input, "persona-b") {
				personaID = "fixture-b"
			}
			scope := pipe.ConversationScope()
			return pipeline.ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{SessionID: scope.SessionID, ConversationEpoch: scope.ConversationEpoch, ResponseID: rand.Text(), PersonaID: personaID, DisplayName: personaID}}, nil
		}
		return &ConversationRuntime{Pipeline: pipe, Bus: bus, Voice: true, Output: true, SessionCfg: cfg, SessionID: sessionID, AgentName: "Synthetic fixture", InputDevice: "SYNTHETIC scripted input; no mic", OutputDevice: "SYNTHETIC stream; no speaker", Cleanup: func() { _ = pipe.CloseWork(); jobs.Close(); _ = player.Close() }}, nil
	}
	cases := []string{"normal fixture", "delayed fixture", "rejected fixture", "fallback fixture", "interrupted fixture", "run a command for synthetic fixture", "normal consume worker fixture", "persona-b fixture"}
	_, err := runProgram(cfg, build, nil, true, func(app App) (tea.Model, error) {
		result, err := tea.NewProgram(matrixTerminal{app: app, inputs: inputs, cases: cases}, tea.WithAltScreen()).Run()
		if final, ok := result.(matrixTerminal); ok {
			if final.index != len(cases) {
				return final.app, fmt.Errorf("owner matrix incomplete%d/%d", final.index, len(cases))
			}
			return final.app, err
		}
		return result, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMatrixCapture(root); err != nil {
		t.Fatal(err)
	}
}
func verifyMatrixCapture(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "captures", "*", "events.jsonl"))
	if err != nil {
		return err
	}
	if len(files) != 1 {
		return fmt.Errorf("matrix requires one capture, got%d", len(files))
	}
	file, err := os.Open(files[0])
	if err != nil {
		return err
	}
	defer file.Close()
	rows := []struct {
		Type  string               `json:"type"`
		Text  string               `json:"text"`
		Trace *events.TraceContext `json:"trace"`
		Span  *events.TraceSpan    `json:"span"`
	}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var row struct {
			Type  string               `json:"type"`
			Text  string               `json:"text"`
			Trace *events.TraceContext `json:"trace"`
			Span  *events.TraceSpan    `json:"span"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	turns := map[string]string{}
	for _, row := range rows {
		if row.Type == "user" && row.Trace != nil && row.Trace.TurnID != nil {
			turns[row.Text] = *row.Trace.TurnID
		}
	}
	seen := map[string]bool{}
	fallback := map[string]string{}
	jobs := map[string]bool{}
	jobCompleted := false
	for _, row := range rows {
		if row.Trace == nil {
			continue
		}
		if row.Trace.JobID != nil && row.Span != nil && row.Span.Stage == "worker" {
			jobs[*row.Trace.JobID] = row.Trace.ParentTurnID != nil && row.Trace.TurnID == nil && row.Trace.ResponseID == nil
			if row.Span.State == "end" {
				if row.Span.Status != "completed" && row.Span.Status != "cancelled" && row.Span.Status != "failed" {
					return fmt.Errorf("noncanonical worker terminal status %q", row.Span.Status)
				}
				jobCompleted = jobCompleted || row.Span.Status == "completed"
			}
		}
		if row.Span == nil || row.Trace.TurnID == nil {
			continue
		}
		span := row.Span
		for input, turn := range turns {
			if turn != *row.Trace.TurnID {
				continue
			}
			if span.Stage == "turn" && span.State == "end" {
				seen[input+":"+span.Status] = true
			}
			if strings.Contains(input, "delayed") && span.Stage == "model" && span.State == "end" && span.StartMS != nil && span.EndMS != nil && *span.EndMS-*span.StartMS >= 300 {
				seen["delay"] = true
			}
			if strings.Contains(input, "fallback") && span.Stage == "synthesis" && span.State == "end" && span.AttemptID != nil {
				fallback[*span.AttemptID] = span.Status
			}
			if strings.Contains(input, "persona-b") && row.Trace.PersonaID != nil && *row.Trace.PersonaID == "fixture-b" {
				seen["persona"] = true
			}
		}
	}
	for _, key := range []string{"normal fixture:completed", "delayed fixture:completed", "rejected fixture:failed", "fallback fixture:completed", "interrupted fixture:interrupted", "delay", "persona"} {
		if !seen[key] {
			return fmt.Errorf("matrix missing%s: %+v", key, seen)
		}
	}
	failed, completed := false, false
	for _, status := range fallback {
		failed = failed || status == "failed"
		completed = completed || status == "completed"
	}
	if !failed || !completed || len(fallback) < 2 {
		return fmt.Errorf("fallback attempts collapsed: %+v", fallback)
	}
	if len(jobs) == 0 || !jobCompleted {
		return errors.New("matrix lacks completed typed job source")
	}
	for _, valid := range jobs {
		if !valid {
			return errors.New("job adopted foreground turn instead of causal parent")
		}
	}
	return nil
}
