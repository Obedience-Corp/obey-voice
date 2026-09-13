package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

var _ brain.LegacyWorkerValidator = (*brain.OllamaBrain)(nil)

type legacyPolicyWorker struct {
	validationErr error
	validated     atomic.Int32
	called        atomic.Int32
	toolErr       error
}

func (w *legacyPolicyWorker) ValidateLegacyWorker(context.Context) error {
	w.validated.Add(1)
	return w.validationErr
}
func (w *legacyPolicyWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *legacyPolicyWorker) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	w.called.Add(1)
	if opts.WorkerBudget != nil {
		if err := opts.WorkerBudget.AllowTool("read_file"); err != nil {
			return "", err
		}
		w.toolErr = opts.WorkerBudget.AllowTool("shell")
	}
	return "done", nil
}
func (*legacyPolicyWorker) ClearHistory()            {}
func (*legacyPolicyWorker) History() []brain.Turn    { return nil }
func (*legacyPolicyWorker) LoadHistory([]brain.Turn) {}

func legacyPolicyAgency(t *testing.T) *Agency {
	t.Helper()
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	a := NewAgency(nil, jobs, nil, &config.Config{}, nil)
	scope := work.WorkScope{SessionID: "session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	c := work.NewCoordinator(scope)
	if _, err := c.Register(scope, work.Admission{JobID: "legacy-job", Task: "task"}); err != nil {
		t.Fatal(err)
	}
	a.workMu.Lock()
	a.coordinator, a.workScope = c, scope
	a.jobScopes["legacy-job"] = scope
	a.workMu.Unlock()
	return a
}

func legacyPolicyConfig(strict bool) *config.Config {
	return &config.Config{
		VoiceWorkerProvider: "ollama",
		VoiceToolsEnabled:   true,
		VoiceWorkers:        &config.VoiceWorkers{Policy: config.WorkerPolicy{ChargePolicy: map[bool]string{true: "strict", false: "advisory"}[strict]}},
	}
}

func TestLegacyOllamaValidationErrorPreventsThinkFull(t *testing.T) {
	a := legacyPolicyAgency(t)
	w := &legacyPolicyWorker{validationErr: errors.New("model unavailable")}
	_, err := a.workerRun("task", w, legacyPolicyConfig(false))(job.WithID(context.Background(), "legacy-job"))
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("worker error = %v, want validation error", err)
	}
	if w.called.Load() != 0 || w.validated.Load() != 1 {
		t.Fatalf("validated=%d ThinkFull=%d, want 1 and 0", w.validated.Load(), w.called.Load())
	}
}

func TestLegacyProductionOllamaValidationRunsBeforeThinkFull(t *testing.T) {
	var showCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"chat-model"}]}`)
		case "/api/show":
			showCalls.Add(1)
			_, _ = io.WriteString(w, `{"remote_host":"https://cloud.example"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	worker, err := brain.NewOllama(&config.Config{OllamaHost: srv.URL, OllamaModel: "chat-model", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a := legacyPolicyAgency(t)
	_, err = a.workerRun("task", worker, legacyPolicyConfig(false))(job.WithID(context.Background(), "legacy-job"))
	if err == nil || !strings.Contains(err.Error(), "cloud alias") {
		t.Fatalf("production worker error = %v, want local validation rejection", err)
	}
	if showCalls.Load() != 1 {
		t.Fatalf("/api/show calls = %d, want 1", showCalls.Load())
	}
}

func TestLegacyStrictPolicyAllowsReadsAndDeniesShell(t *testing.T) {
	a := legacyPolicyAgency(t)
	w := &legacyPolicyWorker{}
	result, err := a.workerRun("task", w, legacyPolicyConfig(true))(job.WithID(context.Background(), "legacy-job"))
	if err != nil || result != "done" {
		t.Fatalf("worker result = %q, %v", result, err)
	}
	if !errors.Is(w.toolErr, brain.ErrWorkerBudgetExceeded) {
		t.Fatalf("shell tool error = %v, want strict budget rejection", w.toolErr)
	}
}

func TestLegacyFeatureDisabledPreservesProviderCompatibility(t *testing.T) {
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	a := NewAgency(nil, jobs, nil, &config.Config{}, nil)
	w := &legacyPolicyWorker{validationErr: errors.New("must not validate")}
	result, err := a.workerRun("task", w, legacyPolicyConfig(true))(context.Background())
	if err != nil || result != "done" {
		t.Fatalf("worker result = %q, %v", result, err)
	}
	if w.validated.Load() != 0 || w.called.Load() != 1 {
		t.Fatalf("validated=%d ThinkFull=%d, want 0 and 1", w.validated.Load(), w.called.Load())
	}
}
