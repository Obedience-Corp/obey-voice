package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

// hookRecorder captures what a tool session reported to the UI hooks.
type hookRecorder struct {
	starts []ToolCall
	ends   []ToolResult
}

func (h *hookRecorder) session() *toolSession {
	return &toolSession{
		routerPolicy: RouterToolsLegacy,
		onStart:      func(c ToolCall) { h.starts = append(h.starts, c) },
		onEnd:        func(r ToolResult) { h.ends = append(h.ends, r) },
	}
}

func readCall(id, path string) api.ToolCall {
	return api.ToolCall{
		ID: id,
		Function: api.ToolCallFunction{
			Name:      "read_file",
			Arguments: mustArgs(map[string]any{"path": path}),
		},
	}
}

func TestExecuteReportsFailureToHooks(t *testing.T) {
	t.Parallel()
	h := &hookRecorder{}
	sess := h.session()

	result := sess.execute(context.Background(), t.TempDir(), readCall("call_1", "missing.txt"))

	if !strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want an error result", result)
	}
	if len(h.starts) != 1 || len(h.ends) != 1 {
		t.Fatalf("hooks fired %d/%d, want one start and one end even on failure", len(h.starts), len(h.ends))
	}
	end := h.ends[0]
	if end.Err == "" || !strings.HasPrefix(end.Err, "error:") {
		t.Errorf("ToolResult.Err = %q, want the leading error line", end.Err)
	}
	if strings.Contains(end.Err, "\n") {
		t.Errorf("ToolResult.Err = %q, want a single line", end.Err)
	}
	if end.ID != "call_1" || end.ID != h.starts[0].ID {
		t.Errorf("ids = %q / %q, want the model id on both", h.starts[0].ID, end.ID)
	}
}

func TestExecuteDeliversCallThenResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &hookRecorder{}
	sess := h.session()

	sess.execute(context.Background(), dir, readCall("call_7", "note.txt"))

	if len(h.starts) != 1 || len(h.ends) != 1 {
		t.Fatalf("hooks fired %d/%d, want one each", len(h.starts), len(h.ends))
	}
	start, end := h.starts[0], h.ends[0]
	if start.ID != "call_7" || end.ID != "call_7" {
		t.Errorf("ids = %q / %q, want call_7", start.ID, end.ID)
	}
	if start.Name != "read_file" || start.Kind != KindRead {
		t.Errorf("start = %+v, want read_file/read", start)
	}
	if start.Summary != "note.txt" {
		t.Errorf("start summary = %q, want the path", start.Summary)
	}
	if end.Err != "" {
		t.Errorf("Err = %q on a successful read, want empty", end.Err)
	}
	if end.Preview == "" {
		t.Errorf("Preview is empty, want a result snippet")
	}
	if end.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want a positive duration", end.Elapsed)
	}
}

func TestExecuteSynthesizesIDWhenModelSendsNone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := &hookRecorder{}
	sess := h.session()

	sess.execute(context.Background(), dir, readCall("", "a.txt"))
	sess.execute(context.Background(), dir, readCall("", "b.txt"))
	sess.execute(context.Background(), dir, api.ToolCall{
		Function: api.ToolCallFunction{
			Name:      "list_files",
			Arguments: mustArgs(map[string]any{"path": "."}),
		},
	})

	var ids []string
	for _, c := range h.starts {
		ids = append(ids, c.ID)
	}
	want := []string{"read_file#1", "read_file#2", "list_files#1"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
		if h.ends[i].ID != want[i] {
			t.Fatalf("end id %d = %q, want %q", i, h.ends[i].ID, want[i])
		}
	}
}

func TestExecuteNormalizesRepeatedProviderIDsWithoutChangingProtocolCall(t *testing.T) {
	t.Parallel()
	h := &hookRecorder{}
	sess := h.session()
	raw := runCommandCall("provider-call", "printf ok")
	sess.execute(context.Background(), t.TempDir(), raw)
	sess.execute(context.Background(), t.TempDir(), raw)
	// This raw id intentionally collides with the synthesized id that would be
	// produced for a model call with no id.
	collision := runCommandCall("run_command#1", "printf ok")
	sess.execute(context.Background(), t.TempDir(), collision)
	sess.execute(context.Background(), t.TempDir(), api.ToolCall{
		Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "printf ok"})},
	})
	if raw.ID != "provider-call" || collision.ID != "run_command#1" {
		t.Fatalf("protocol ids mutated: raw=%q collision=%q", raw.ID, collision.ID)
	}
	if len(h.starts) != 4 || len(h.ends) != 4 {
		t.Fatalf("hooks fired %d/%d, want four starts and ends", len(h.starts), len(h.ends))
	}
	seen := make(map[string]bool)
	for i := range h.starts {
		if h.starts[i].ID == "" || seen[h.starts[i].ID] {
			t.Fatalf("start %d id=%q, want unique non-empty hook ids (%v)", i, h.starts[i].ID, h.starts)
		}
		seen[h.starts[i].ID] = true
		if h.ends[i].ID != h.starts[i].ID {
			t.Fatalf("call %d start/end ids=%q/%q, want matching", i, h.starts[i].ID, h.ends[i].ID)
		}
	}
	if h.starts[0].ID != "provider-call" || h.starts[1].ID != "provider-call#2" {
		t.Fatalf("repeated provider ids=%q,%q, want provider-call/provider-call#2", h.starts[0].ID, h.starts[1].ID)
	}
	if h.starts[2].ID != "run_command#1" || h.starts[3].ID == "run_command#1" {
		t.Fatalf("collision ids=%q,%q, want distinct", h.starts[2].ID, h.starts[3].ID)
	}
}

func runCommandCall(id, command string) api.ToolCall {
	return api.ToolCall{ID: id, Function: api.ToolCallFunction{
		Name: "run_command", Arguments: mustArgs(map[string]any{"command": command}),
	}}
}

func TestExecuteCommandPublishesObservedOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		command    string
		timeout    time.Duration
		status     string
		exit       *int
		wantErr    bool
		wantOutput string
	}{
		{name: "success", command: "printf 'héllo'", status: "succeeded", exit: intPtr(0), wantOutput: "héllo"},
		{name: "failure wrapped in json", command: "printf 'bad'; exit 7", status: "failed", exit: intPtr(7), wantErr: true, wantOutput: "bad"},
		{name: "timeout", command: "sleep 1", timeout: 10 * time.Millisecond, status: "timed_out", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &hookRecorder{}
			sess := h.session()
			sess.commandTimeout = tt.timeout
			result := sess.execute(context.Background(), t.TempDir(), runCommandCall("cmd-1", tt.command))
			if len(h.ends) != 1 || h.ends[0].Outcome == nil {
				t.Fatalf("result=%q ends=%+v, want one typed outcome", result, h.ends)
			}
			outcome := h.ends[0].Outcome
			if string(outcome.Status) != tt.status {
				t.Fatalf("status=%q, want %q", outcome.Status, tt.status)
			}
			if tt.exit == nil && outcome.ExitCode != nil {
				t.Fatalf("exit code=%v, want nil", *outcome.ExitCode)
			}
			if tt.exit != nil && (outcome.ExitCode == nil || *outcome.ExitCode != *tt.exit) {
				t.Fatalf("exit code=%v, want %v", outcome.ExitCode, *tt.exit)
			}
			if tt.wantErr && h.ends[0].Err == "" {
				t.Fatal("legacy Err empty for observed failure")
			}
			if tt.wantOutput != "" && !strings.Contains(outcome.OutputPreview, tt.wantOutput) {
				t.Fatalf("output preview=%q, want %q", outcome.OutputPreview, tt.wantOutput)
			}
			if strings.Contains(h.ends[0].Preview, `"command"`) {
				t.Fatalf("hook preview exposed JSON envelope: %q", h.ends[0].Preview)
			}
		})
	}
}

func TestExecuteCommandPreservesCancellationAndSignalFacts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &hookRecorder{}
	sess := h.session()
	sess.execute(ctx, t.TempDir(), runCommandCall("cancel-1", "sleep 1"))
	if got := h.ends[0].Outcome.Status; got != "canceled" {
		t.Fatalf("canceled status=%q, want canceled", got)
	}
	if h.ends[0].Outcome.ExitCode != nil {
		t.Fatalf("canceled exit code=%v, want nil", *h.ends[0].Outcome.ExitCode)
	}

	h = &hookRecorder{}
	sess = h.session()
	sess.execute(context.Background(), t.TempDir(), runCommandCall("signal-1", "kill -TERM $$"))
	if got := h.ends[0].Outcome.Status; got != "failed" {
		t.Fatalf("signal status=%q, want failed", got)
	}
	if h.ends[0].Outcome.Signal == "" {
		t.Fatal("signal fact empty, want observed termination signal")
	}
}

func TestToolSessionRefusesOutsideTheBoundaryAndReportsIt(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "campaigns", "demo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, ".grok", "sessions", "x.jsonl")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &hookRecorder{}
	sess := rec.session()
	sess.boundary = Boundary{Root: root, home: home}

	got := sess.execute(context.Background(), root, readCall("r1", outside))
	if !strings.HasPrefix(got, "error:") || !strings.Contains(got, "outside the work boundary") || strings.Contains(got, "secret") {
		t.Fatalf("read outside = %q, want a refusal and no contents", got)
	}
	if len(rec.ends) != 1 || rec.ends[0].Err == "" || rec.ends[0].Outcome == nil || rec.ends[0].Outcome.Status != "failed" || rec.ends[0].Outcome.ExitCode != nil {
		t.Fatalf("hooks = %+v, want typed refusal without claimed process exit", rec.ends)
	}

	run := api.ToolCall{ID: "c1", Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "cat " + outside})}}
	if got := sess.execute(context.Background(), root, run); !strings.Contains(got, "outside the work boundary") {
		t.Fatalf("shell outside = %q, want refused", got)
	}
	inside := api.ToolCall{ID: "c2", Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "echo inside"})}}
	if got := sess.execute(context.Background(), root, inside); !strings.Contains(got, "inside") || strings.Contains(got, "boundary") {
		t.Fatalf("shell inside = %q, want it to run", got)
	}
	if got := sess.execute(context.Background(), root, readCall("r2", "README.md")); strings.Contains(got, "boundary") {
		t.Fatalf("read inside = %q, want an ordinary result", got)
	}
}

// The two escapes the review reproduced through the tools themselves: a
// dangling link that a write would follow, and a link with ".." behind it
// that the OS resolves in order. Neither may touch or read the outside.
func TestToolSessionCannotFollowADanglingOrParentLinkOutside(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "campaigns", "demo")
	other := filepath.Join(home, "other")
	if err := os.MkdirAll(filepath.Join(other, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "secret"), []byte("secret-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "new-outside-file")
	if err := os.Symlink(outside, filepath.Join(root, "dangling")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(other, "sub"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	rec := &hookRecorder{}
	sess := rec.session()
	sess.boundary = Boundary{Root: root, home: home}

	write := func(id, path string) api.ToolCall {
		return api.ToolCall{ID: id, Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": path, "content": "escaped"})}}
	}
	if got := sess.execute(context.Background(), root, write("w1", "dangling")); !strings.Contains(got, "outside the work boundary") {
		t.Fatalf("write through a dangling link = %q, want refused", got)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file was created through the dangling link")
	}
	got := sess.execute(context.Background(), root, readCall("r1", root+"/linked/../secret"))
	if !strings.Contains(got, "outside the work boundary") || strings.Contains(got, "secret-content") {
		t.Fatalf("read through link/.. = %q, want refused with no contents", got)
	}
	if got := sess.execute(context.Background(), root, write("w2", root+"/linked/../newfile")); !strings.Contains(got, "outside the work boundary") {
		t.Fatalf("write through link/.. = %q, want refused", got)
	}
	if _, err := os.Stat(filepath.Join(other, "newfile")); err == nil {
		t.Fatal("the outside file was created through link/..")
	}
	for _, r := range rec.ends {
		if r.Err == "" {
			t.Fatalf("hook for %s reported no error", r.ID)
		}
	}
}
