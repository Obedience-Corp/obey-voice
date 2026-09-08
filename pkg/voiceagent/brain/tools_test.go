package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if len(rec.ends) != 1 || rec.ends[0].Err == "" {
		t.Fatalf("hooks = %+v, want the refusal reported as the tool error", rec.ends)
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
