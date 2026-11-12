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

// heldCodexBrain is a Codex persona whose requests wait for the test.
func heldCodexBrain(t *testing.T) (*CodexBrain, *heldCodex, context.Context) {
	t.Helper()
	f := newHeldCodex()
	c := &CodexBrain{client: f, cfg: &config.Config{AgentName: "Obey", ActivePersona: "helper", MaxHistory: 20}, systemPrompt: "Persona fixture", turnInstruction: "Speak briefly."}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return c, f, ctx
}

// whileRunning starts a streamed request, runs during once Codex holds it,
// lets it finish and returns its error.
func (f *heldCodex) whileRunning(t *testing.T, ctx context.Context, c *CodexBrain, input string, opts StreamOptions, during func()) error {
	t.Helper()
	stream, err := c.ThinkStream(ctx, input, opts)
	if err != nil {
		t.Fatal(err)
	}
	<-f.started
	during()
	f.release <- struct{}{}
	_, res := collectStream(t, stream)
	return res.Err
}

// next runs one more request and returns what Codex was sent.
func (f *heldCodex) next(t *testing.T, ctx context.Context, c *CodexBrain, input string) codexRequest {
	t.Helper()
	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, input, StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func TestCodexJobResultRecordedWhileANewThreadStartsReachesTheNextRequestOnce(t *testing.T) {
	c, f, ctx := heldCodexBrain(t)
	if err := f.whileRunning(t, ctx, c, "Remember cobalt.", StreamOptions{}, func() {
		if err := c.AppendAssistantResponseWithIdentity("Job result: the report is ready.", TurnIdentity{PersonaID: "helper"}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	next := f.next(t, ctx, c, "Anything new?")
	if next.ThreadID != "" {
		t.Fatalf("the thread started without the job result was resumed: %q", next.ThreadID)
	}
	for _, line := range []string{"the report is ready.", "Remember cobalt."} {
		if got := strings.Count(next.Prompt, line); got != 1 {
			t.Fatalf("%q reached the next request %d times:\n%s", line, got, next.Prompt)
		}
	}
}

func TestCodexClearWhileARequestRunsStartsANewThread(t *testing.T) {
	c, f, ctx := heldCodexBrain(t)
	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Remember cobalt.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started
	if err := f.whileRunning(t, ctx, c, "What next?", StreamOptions{}, c.ClearHistory); err != nil {
		t.Fatal(err)
	}
	next := f.next(t, ctx, c, "Start over.")
	if next.ThreadID != "" || strings.Contains(next.Prompt, "Remember cobalt.") {
		t.Fatalf("a cleared conversation resumed its thread or kept what came before: %+v", next)
	}
}

// A view imported while a request runs, here of the same length as the one
// the request was built on, replaces the conversation: the request's thread
// holds the old one, so it is not resumed.
func TestCodexImportWhileARequestRunsStartsANewThread(t *testing.T) {
	c, f, ctx := heldCodexBrain(t)
	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Remember cobalt.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started
	other := []Turn{{Role: "user", Content: "Talk about teal."}, {Role: "assistant", Content: "Teal it is.", PersonaID: "rex", PersonaDisplayName: "Rex"}}
	if err := f.whileRunning(t, ctx, c, "What next?", StreamOptions{}, func() {
		if err := c.ImportContinuity(ContinuitySnapshot{Canonical: other, Working: other}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	next := f.next(t, ctx, c, "And now?")
	if next.ThreadID != "" || !strings.Contains(next.Prompt, "Talk about teal.") || strings.Contains(next.Prompt, "Remember cobalt.") {
		t.Fatalf("the thread of a replaced conversation was resumed, or the imported view not sent: %+v", next)
	}
}

// A request on a copy of the view, as compaction makes, runs in a thread of
// its own: a job result recorded meanwhile goes to the live thread, which the
// next request resumes and sends it once, without the copy's request.
func TestCodexTransientRequestLeavesTheLiveThreadToItsOwnLines(t *testing.T) {
	c, f, ctx := heldCodexBrain(t)
	f.release <- struct{}{}
	if _, err := c.ThinkFull(ctx, "Remember cobalt.", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	<-f.started
	live := c.SessionInfo().ID
	if err := f.whileRunning(t, ctx, c, "Summarize the conversation.", StreamOptions{TransientHistory: true, OmitTurnInstruction: true}, func() {
		if err := c.AppendAssistantResponseWithIdentity("Job result: the report is ready.", TurnIdentity{PersonaID: "helper"}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	summary := f.requests[1]
	f.mu.Unlock()
	if summary.ThreadID != "" || !summary.Ephemeral || !strings.Contains(summary.Prompt, "Remember cobalt.") {
		t.Fatalf("the transient request = %+v", summary)
	}
	next := f.next(t, ctx, c, "Anything new?")
	if next.ThreadID != live || strings.Count(next.Prompt, "the report is ready.") != 1 || strings.Contains(next.Prompt, "Summarize the conversation.") {
		t.Fatalf("live thread %q, next request %+v", live, next)
	}
	for _, turn := range c.History() {
		if turn.Content == "Summarize the conversation." {
			t.Fatalf("the transient request was recorded: %+v", c.History())
		}
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

func TestCodexLogsWhyAViewDropsItsThread(t *testing.T) {
	logs := captureLogs(t)
	c, _ := codexFixture(t)
	c.cfg.MaxHistory = 20
	if _, err := c.ThinkFull(t.Context(), "Remember cobalt.", StreamOptions{Speaker: "speaker-2"}); err != nil {
		t.Fatal(err)
	}
	held := c.ContinuitySnapshot()
	extended := held
	extended.Working = append(deepCopyTurns(held.Working), Turn{Role: "user", Content: "And teal."})
	if err := c.ImportContinuity(extended); err != nil {
		t.Fatal(err)
	}
	if c.SessionInfo().ID != "thread-fixture" || len(logs.find("codex thread dropped")) != 0 {
		t.Fatalf("a view adding a line dropped the thread: %+v %+v", c.SessionInfo(), logs.find("codex thread dropped"))
	}

	changed := c.ContinuitySnapshot()
	changed.Working[0].Content = "Remember amber."
	if err := c.ImportContinuity(changed); err != nil {
		t.Fatal(err)
	}
	got := logs.find("codex thread dropped")
	if c.SessionInfo().ID != "" || len(got) != 1 || got[0]["thread_id"] != "thread-fixture" || got[0]["reason"] != "line 0 of the view (user) differs from the line the thread holds" {
		t.Fatalf("thread %q, drop log %+v", c.SessionInfo().ID, got)
	}
}
