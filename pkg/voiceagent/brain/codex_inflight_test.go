package brain

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// heldCodex holds each request until the test releases it.
type heldCodex struct {
	mu       sync.Mutex
	requests []codexRequest
	started  chan struct{}
	release  chan struct{}
}

func newHeldCodex() *heldCodex {
	return &heldCodex{started: make(chan struct{}, 8), release: make(chan struct{}, 8)}
}

func (f *heldCodex) run(ctx context.Context, r codexRequest, emit func(string) error, _ StreamOptions) (codexResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	n := len(f.requests)
	f.mu.Unlock()
	f.started <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
		return codexResult{}, ctx.Err()
	}
	if err := emit("Noted."); err != nil {
		return codexResult{}, err
	}
	return codexResult{ThreadID: fmt.Sprintf("thread-%d", n)}, nil
}

func (f *heldCodex) request(t *testing.T, i int) codexRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("codex requests = %d, want more than %d", len(f.requests), i)
	}
	return f.requests[i]
}

func TestCodexJobResultRecordedWhileARequestRunsReachesTheNextRequestOnce(t *testing.T) {
	f := newHeldCodex()
	c := &CodexBrain{client: f, cfg: &config.Config{AgentName: "Obey", ActivePersona: "helper", MaxHistory: 20}, systemPrompt: "Persona fixture", turnInstruction: "Speak briefly."}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Remember cobalt.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started

	stream, err := c.ThinkStream(ctx, "What next?", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	recorded := make(chan error, 1)
	go func() {
		recorded <- c.AppendAssistantResponseWithIdentity("Job result: the report is ready.", TurnIdentity{PersonaID: "helper", PersonaDisplayName: "Helper"})
	}()
	select {
	case err := <-recorded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recording a job result waited for the request in flight")
	}
	f.release <- struct{}{}
	if _, res := collectStream(t, stream); res.Err != nil {
		t.Fatal(res.Err)
	}

	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Anything new?", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	next := f.request(t, 2)
	if got := strings.Count(next.Prompt, "the report is ready."); got != 1 {
		t.Fatalf("the job result reached the next request %d times:\n%s", got, next.Prompt)
	}
	if roles := len(c.History()); roles != 7 {
		t.Fatalf("history = %+v", c.History())
	}
}

// capturedLogs collects what is logged through slog while a test runs.
type capturedLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

func captureLogs(t *testing.T) *capturedLogs {
	t.Helper()
	logs := &capturedLogs{}
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func (l *capturedLogs) Enabled(context.Context, slog.Level) bool { return true }
func (l *capturedLogs) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *capturedLogs) WithGroup(string) slog.Handler            { return l }
func (l *capturedLogs) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r.Clone())
	return nil
}

// find returns the attributes of each record logged with message.
func (l *capturedLogs) find(message string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, r := range l.records {
		if r.Message != message {
			continue
		}
		attrs := map[string]any{"level": r.Level}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

func TestCodexLogsARequestOverTheSizeThresholdWithItsSize(t *testing.T) {
	logs := captureLogs(t)
	c, f := codexFixture(t)
	c.cfg.MaxHistory = 20
	if _, err := c.ThinkFull(t.Context(), "A short line.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := logs.find("codex request is large"); len(got) != 0 {
		t.Fatalf("a short request was logged as large: %+v", got)
	}
	for range 3 {
		if err := c.AppendAssistantResponseWithIdentity(strings.Repeat("teal ", 5000), TurnIdentity{PersonaID: "rex", PersonaDisplayName: "Rex"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.ThinkFull(t.Context(), "Catch up.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	got := logs.find("codex request is large")
	size := len(f.requests[len(f.requests)-1].Prompt)
	if len(got) != 1 || got[0]["bytes"] != int64(size) || got[0]["threshold"] != int64(codexLargeRequestBytes) || got[0]["resumed"] != true || got[0]["level"] != slog.LevelWarn {
		t.Fatalf("large request log = %+v, want one warning of %d bytes", got, size)
	}
}
