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
