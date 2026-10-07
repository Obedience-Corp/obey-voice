package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type appTestBrain struct{ history []brain.Turn }

func (*appTestBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (*appTestBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (b *appTestBrain) ClearHistory()                  { b.history = nil }
func (b *appTestBrain) History() []brain.Turn          { return b.history }
func (b *appTestBrain) LoadHistory(turns []brain.Turn) { b.history = turns }

type appPauseSession struct {
	events chan stt.Event
	calls  chan struct{}
}

func (s *appPauseSession) Events() <-chan stt.Event                            { return s.events }
func (s *appPauseSession) Close() error                                        { return nil }
func (s *appPauseSession) BindCompletionCoordinator(*work.Coordinator)         {}
func (s *appPauseSession) HasCapturedInput() bool                              { return false }
func (s *appPauseSession) ResumeCapture(context.Context, stt.PauseToken) error { return nil }
func (s *appPauseSession) PauseForCompletion(context.Context, work.WorkScope, string) (stt.PauseResult, error) {
	select {
	case s.calls <- struct{}{}:
	default:
	}
	s.events <- stt.YieldedForCompletion{}
	return stt.PauseResult{Decision: stt.PauseAcknowledged}, nil
}

type appPauseSTT struct{ session *appPauseSession }

func (s appPauseSTT) Start(context.Context) (stt.Session, error) { return s.session, nil }
func (s appPauseSTT) Available() bool                            { return true }

func TestClassifyVoiceFailure(t *testing.T) {
	transient := errors.New("STT: stream reset failed")

	tests := []struct {
		name                string
		err                 error
		ctxErr              error
		consecutiveFailures int
		want                VoiceFailureAction
	}{
		{"context canceled error stops", context.Canceled, nil, 1, VoiceShutdown},
		{"wrapped context canceled stops", fmt.Errorf("brain: %w", context.Canceled), nil, 1, VoiceShutdown},
		{"cancelled context wins over transient error", transient, context.Canceled, 1, VoiceShutdown},
		{"first transient failure retries", transient, nil, 1, VoiceRetry},
		{"second transient failure retries", transient, nil, 2, VoiceRetry},
		{"sustained failures fall back", transient, nil, maxVoiceFailures, VoiceFallback},
		{"beyond threshold falls back", transient, nil, maxVoiceFailures + 1, VoiceFallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyVoiceFailure(tt.err, tt.ctxErr, tt.consecutiveFailures); got != tt.want {
				t.Fatalf("ClassifyVoiceFailure(%v, %v, %d) = %d, want %d",
					tt.err, tt.ctxErr, tt.consecutiveFailures, got, tt.want)
			}
		})
	}
}

func TestClassifyVoiceTurnFailureDoesNotCountResponseErrorsAsInputFailures(t *testing.T) {
	responseErr := errors.New("admit response: roster collision")
	if got := ClassifyVoiceTurnFailure(responseErr, nil, "Veronica", maxVoiceFailures); got != VoiceRetry {
		t.Fatalf("recognized transcript at the fallback threshold classified as %d, want retry", got)
	}
	if got := ClassifyVoiceTurnFailure(responseErr, context.Canceled, "Veronica", maxVoiceFailures); got != VoiceShutdown {
		t.Fatalf("canceled response failure classified as %d, want shutdown", got)
	}
}

type fallbackTestSTT struct {
	attempts      int
	inputFailures int
}

func (s *fallbackTestSTT) Available() bool { return true }
func (s *fallbackTestSTT) Start(context.Context) (stt.Session, error) {
	s.attempts++
	if s.attempts <= s.inputFailures {
		return nil, errors.New("microphone unavailable")
	}
	events := make(chan stt.Event, 1)
	events <- stt.FinalTranscript{Text: "Veronica"}
	close(events)
	return &appPauseSession{events: events}, nil
}

func TestRunVoiceFallbackCountsOnlyConsecutiveInputFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		inputFailures int
		wantErrors    int
		wantFallback  bool
	}{
		{name: "successful capture resets two failures", inputFailures: 2, wantErrors: 5},
		{name: "three input failures switch to text", inputFailures: 3, wantErrors: 3, wantFallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			source := &fallbackTestSTT{inputFailures: tc.inputFailures}
			bus := events.NewBus()
			var surfaced int
			var fallback bool
			events.Subscribe(bus, func(events.Error) {
				surfaced++
				if !tc.wantFallback && surfaced == tc.wantErrors {
					cancel()
				}
			})
			events.Subscribe(bus, func(info events.Info) {
				fallback = fallback || strings.Contains(info.Message, "switching to text")
			})
			p := &pipeline.Pipeline{Brain: &appTestBrain{}, STT: source, Events: bus}
			p.AdmitInput = func(context.Context, string, bool) (pipeline.ResponseAdmission, error) {
				return pipeline.ResponseAdmission{}, errors.New("ambiguous persona")
			}
			if err := Run(ctx, p, strings.NewReader(""), false, true); err != nil {
				t.Fatal(err)
			}
			if fallback != tc.wantFallback || surfaced != tc.wantErrors || source.attempts != tc.wantErrors {
				t.Fatalf("fallback=%v, errors=%d, attempts=%d; want fallback=%v and %d errors/attempts", fallback, surfaced, source.attempts, tc.wantFallback, tc.wantErrors)
			}
		})
	}
}

func TestNormalizeCommand(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"Goodbye.", "goodbye"},
		{"GOODBYE!", "goodbye"},
		{"  exit  ", "exit"},
		{"talk later...", "talk later"},
		{"Reset?", "reset"},
		{"what's up", "what's up"}, // internal punctuation kept
		{"", ""},
	} {
		if got := NormalizeCommand(tt.in); got != tt.want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestVoiceCommandMatching(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		exit  bool
		clear bool
	}{
		// Sentences that merely mention a phrase must not trigger anything.
		{"router question does not clear", "Can you help me reset my router password?", false, false},
		{"mention of goodbye does not exit", "How do you say goodbye in French?", false, false},
		{"regular speech passes through", "Tell me about summer weather", false, false},
		{"punctuated goodbye exits", "Goodbye.", true, false},
		{"shouted goodbye exits", "GOODBYE!", true, false},
		{"multi-word exit phrase", "Talk later.", true, false},
		{"exact reset clears", "reset", false, true},
		{"punctuated reset clears", "Reset.", false, true},
		{"slash clear clears", "/clear", false, true},
		{"fresh start clears", "fresh start", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NormalizeCommand(tt.in)
			if got := IsExitCommand(cmd); got != tt.exit {
				t.Errorf("exitPhrases[NormalizeCommand(%q)] = %v, want %v", tt.in, got, tt.exit)
			}
			if got := IsClearCommand(cmd); got != tt.clear {
				t.Errorf("IsClearCommand(NormalizeCommand(%q)) = %v, want %v", tt.in, got, tt.clear)
			}
		})
	}
}

func TestIsResumeVoiceCommand(t *testing.T) {
	for _, tt := range []struct {
		cmd  string
		want bool
	}{
		{"/voice", true},
		{"/v", true},
		{"voice", false},
		{"/voices", false},
		{"hello", false},
		{"", false},
	} {
		if got := IsResumeVoiceCommand(tt.cmd); got != tt.want {
			t.Errorf("IsResumeVoiceCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// TestLineReaderNextCancels: next unblocks when ctx is cancelled mid-wait.
func TestLineReaderNextCancels(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close() // unblock the reader goroutine when the test ends

	ctx, cancel := context.WithCancel(context.Background())
	lr := newLineReader(ctx, pr)

	type result struct {
		line string
		ok   bool
	}
	resCh := make(chan result, 1)
	go func() {
		line, ok := lr.next(ctx)
		resCh <- result{line, ok}
	}()

	cancel()

	select {
	case r := <-resCh:
		if r.ok {
			t.Fatalf("expected ok=false after cancellation, got line=%q ok=%v", r.line, r.ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lineReader.next did not return within 2s after context cancellation")
	}
}

func TestLineReaderBackgroundPollKeepsQueuedLinePriority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lr := &lineReader{lines: make(chan string, 1)}
	lr.lines <- "user line"
	called := false
	line, ok := lr.nextWithBackground(ctx, func() error {
		called = true
		return nil
	})
	if !ok || line != "user line" || called {
		t.Fatalf("line=%q ok=%v backgroundCalled=%v", line, ok, called)
	}
}

func TestLineReaderBackgroundPollsWhileIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lr := &lineReader{lines: make(chan string)}
	called := make(chan struct{}, 1)
	go func() {
		_, _ = lr.nextWithBackground(ctx, func() error {
			select {
			case called <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	select {
	case <-called:
		cancel()
	case <-time.After(1500 * time.Millisecond):
		cancel()
		t.Fatal("background poll did not run")
	}
}

func TestRunClearUsesConversationAuthorityAndCheckpoints(t *testing.T) {
	p := &pipeline.Pipeline{Brain: &appTestBrain{}, Events: events.NewBus()}
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	t.Cleanup(func() { _ = p.CloseWork() })
	a := pipeline.NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	if err := p.EnableConversationWork("app-session", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	old := p.WorkCoordinator().Scope()
	key, err := p.WorkCoordinator().Register(old, work.Admission{Task: "old", Origin: "agency"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.WorkCoordinator().Publish(key, work.Outcome{Scope: old, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "old result"}); err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	p.OnTurn = func() { checkpoints++ }
	if err := Run(context.Background(), p, strings.NewReader("/clear\nexit\n"), true, false); err != nil {
		t.Fatal(err)
	}
	now := p.WorkCoordinator().Scope()
	if now == old || len(p.WorkCoordinator().Pending(now)) != 0 {
		t.Fatalf("clear retained old scope/results: old=%+v now=%+v pending=%v", old, now, p.WorkCoordinator().Pending(now))
	}
	if checkpoints != 1 {
		t.Fatalf("OnTurn checkpoints=%d, want 1", checkpoints)
	}
}

func TestRunVoiceTurnMonitorsReadyWorkWithoutCancelingTurn(t *testing.T) {
	p := &pipeline.Pipeline{Brain: &appTestBrain{}, Events: events.NewBus()}
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	t.Cleanup(func() { _ = p.CloseWork() })
	a := pipeline.NewAgency(p, jobs, nil, &config.Config{}, nil)
	a.Wire()
	if err := p.EnableConversationWork("voice-session", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	scope := p.WorkCoordinator().Scope()
	key, err := p.WorkCoordinator().Register(scope, work.Admission{Task: "ready", Origin: "agency"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.WorkCoordinator().Publish(key, work.Outcome{Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "ready"}); err != nil {
		t.Fatal(err)
	}
	session := &appPauseSession{events: make(chan stt.Event, 2), calls: make(chan struct{}, 1)}
	p.STT = appPauseSTT{session: session}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := runVoiceTurn(ctx, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.calls:
	case <-time.After(time.Second):
		t.Fatal("ready work did not invoke capture pause")
	}
}

// TestRunReturnsOnCancel: Run unwinds when ctx is cancelled while awaiting input.
func TestRunReturnsOnCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	p := &pipeline.Pipeline{Events: events.NewBus()}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p, pr, true /* textMode */, false)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancellation")
	}
}

// failingSTT always fails to start a session, so every voice turn errors.
type failingSTT struct{}

func (failingSTT) Start(context.Context) (stt.Session, error) {
	return nil, errors.New("stt unavailable")
}

func (failingSTT) Available() bool { return true }

// TestRunFallsBackToTextAfterSustainedVoiceFailures: with an always-failing STT,
// Run retries then falls back to text input, where the queued "exit" ends it.
func TestRunFallsBackToTextAfterSustainedVoiceFailures(t *testing.T) {
	p := &pipeline.Pipeline{Events: events.NewBus(), STT: failingSTT{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, p, strings.NewReader("exit\n"), false /* textMode */, false)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run never fell back to text input after sustained voice failures")
	}
}
