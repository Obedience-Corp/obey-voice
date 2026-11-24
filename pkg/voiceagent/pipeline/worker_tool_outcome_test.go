package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/internal/searchendpoint"
)

func toolReply(name string, args map[string]any) api.ChatResponse {
	a := api.NewToolCallFunctionArguments()
	for k, v := range args {
		a.Set(k, v)
	}
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: name, Arguments: a}}}}}
}

// A worker's file, web and skill tool calls fold as succeeded or failed, never
// unknown, through the hooks, events and fold a running job uses.
func TestWorkerToolActivityCountsEachOutcome(t *testing.T) {
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("q") == "broken" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `<html><body><div class="result"><a class="result__a" href="https://example.com/a">A title</a></div></body></html>`)
	}))
	t.Cleanup(search.Close)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<html><head><title>A page</title></head><body>Hello.</body></html>`)
	}))
	t.Cleanup(page.Close)
	skillsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsDir, "greet"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "greet", "SKILL.md"), []byte("---\nname: greet\ndescription: Greets people.\n---\nSay hello.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, tool string
		args       map[string]any
		activity   string
		http       int
	}{
		{name: "list", tool: "list_files", args: map[string]any{"path": "."}, activity: "listed 1 folder (1 succeeded)"},
		{name: "list missing", tool: "list_files", args: map[string]any{"path": "gone"}, activity: "listed 1 folder (1 failed)"},
		{name: "read", tool: "read_file", args: map[string]any{"path": "notes.txt"}, activity: "read 1 file (1 succeeded)"},
		{name: "read error-looking text", tool: "read_file", args: map[string]any{"path": "looks-failed.txt"}, activity: "read 1 file (1 succeeded)"},
		{name: "read missing", tool: "read_file", args: map[string]any{"path": "missing.txt"}, activity: "read 1 file (1 failed)"},
		{name: "write", tool: "write_file", args: map[string]any{"path": "out/new.txt", "content": "new"}, activity: "wrote 1 file (1 succeeded)"},
		{name: "write under a file", tool: "write_file", args: map[string]any{"path": "notes.txt/new.txt", "content": "new"}, activity: "wrote 1 file (1 failed)"},
		{name: "search", tool: "web_search", args: map[string]any{"query": "found"}, activity: "searched 1 time (1 succeeded)", http: 200},
		{name: "search answered 500", tool: "web_search", args: map[string]any{"query": "broken"}, activity: "searched 1 time (1 failed)", http: 500},
		{name: "fetch", tool: "fetch_url", args: map[string]any{"url": page.URL}, activity: "fetched 1 page (1 succeeded)", http: 200},
		{name: "fetch answered 404", tool: "fetch_url", args: map[string]any{"url": page.URL + "/missing"}, activity: "fetched 1 page (1 failed)", http: 404},
		{name: "skill", tool: "read_skill", args: map[string]any{"name": "greet"}, activity: "loaded 1 skill (1 succeeded)"},
		{name: "unknown skill", tool: "read_skill", args: map[string]any{"name": "nope"}, activity: "loaded 1 skill (1 failed)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := steerSetup{jobs: searchendpoint.With(context.Background(), search.URL), skillsDir: skillsDir}
			h := newSteerJobHarnessWith(t, setup, func(_ int, req api.ChatRequest) (api.ChatResponse, int) {
				if last := req.Messages[len(req.Messages)-1]; last.Role == "tool" {
					return textReply("Done."), 0
				}
				return toolReply(tc.tool, tc.args), 0
			})
			if err := os.WriteFile(filepath.Join(h.root, "looks-failed.txt"), []byte("error: not really"), 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var ended []events.ToolCallFinished
			events.Subscribe(h.p.Events, func(e events.ToolCallFinished) {
				mu.Lock()
				ended = append(ended, e)
				mu.Unlock()
			})
			id := h.spawn("use one tool")
			h.awaitFinished(id)
			if got := h.a.work.Summary(id); got != tc.activity {
				t.Fatalf("activity = %q, want %q", got, tc.activity)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(ended) != 1 || ended[0].JobID != id || ended[0].Outcome == nil {
				t.Fatalf("tool ends = %+v", ended)
			}
			got := 0
			if s := ended[0].Outcome.HTTPStatus; s != nil {
				got = *s
			}
			if got != tc.http {
				t.Fatalf("http status = %d, want %d", got, tc.http)
			}
		})
	}
}
