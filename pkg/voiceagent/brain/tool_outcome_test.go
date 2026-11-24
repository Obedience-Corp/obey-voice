package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/internal/searchendpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

func toolCallFor(name string, args map[string]any) api.ToolCall {
	return api.ToolCall{ID: name + "-1", Function: api.ToolCallFunction{Name: name, Arguments: mustArgs(args)}}
}

func searchServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("q") {
		case "broken":
			http.Error(w, "down", http.StatusInternalServerError)
		case "nothing":
			fmt.Fprint(w, `<html><body></body></html>`)
		default:
			fmt.Fprint(w, `<html><body><div class="result"><a class="result__a" href="https://example.com/a">A title</a><a class="result__snippet">A snippet.</a></div></body></html>`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// The model-facing text of every host-run file, web and skill tool is the
// text it was before outcomes were observed, and the hooks get the outcome
// the tool itself saw: a file that reads "error: …" was still read.
func TestToolResultsKeepTheirTextAndReportTheirOutcome(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, body := range map[string]string{"note.txt": "hello\nworld", "looks-failed.txt": "error: not really", "blocker": "a file"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	png := writePNG(t, filepath.Join(dir, "shot.png"))
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Listing</title></head><body><p>Price: $20 million</p></body></html>`)
	}))
	t.Cleanup(page.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	search := searchServer(t)
	catalog := []skills.Skill{{Name: "hello", Body: "Say hello warmly.", Dir: "/skills/hello"}}
	missing := filepath.Join(dir, "missing.txt")
	gone := filepath.Join(dir, "gone")
	blocker := filepath.Join(dir, "blocker")
	const ok, failed = events.ToolOutcomeSucceeded, events.ToolOutcomeFailed

	cases := []struct {
		name   string
		call   api.ToolCall
		result string
		prefix bool
		err    string
		status events.ToolOutcomeStatus
		http   int
	}{
		{name: "list ok", call: toolCallFor("list_files", map[string]any{"path": "."}),
			result: "blocker\nlooks-failed.txt\nnote.txt\nshot.png\nsub/", status: ok},
		{name: "list missing", call: toolCallFor("list_files", map[string]any{"path": "gone"}),
			result: "error: open " + gone + ": no such file or directory", err: "error: open " + gone + ": no such file or directory", status: failed},
		{name: "read ok", call: toolCallFor("read_file", map[string]any{"path": "note.txt"}), result: "hello\nworld", status: ok},
		{name: "read error-looking text", call: toolCallFor("read_file", map[string]any{"path": "looks-failed.txt"}),
			result: "error: not really", status: ok},
		{name: "read image", call: toolCallFor("read_file", map[string]any{"path": "shot.png"}),
			result: fmt.Sprintf("shot.png is an image file (image/png, %d bytes), not text.", len(png)), status: ok},
		{name: "read missing", call: toolCallFor("read_file", map[string]any{"path": "missing.txt"}),
			result: "error: open " + missing + ": no such file or directory", err: "error: open " + missing + ": no such file or directory", status: failed},
		{name: "read no path", call: toolCallFor("read_file", map[string]any{}), result: "error: path is required", err: "error: path is required", status: failed},
		{name: "write ok", call: toolCallFor("write_file", map[string]any{"path": "sub/out.txt", "content": "fresh"}),
			result: "wrote 5 bytes to sub/out.txt", status: ok},
		{name: "write under a file", call: toolCallFor("write_file", map[string]any{"path": "blocker/x.txt", "content": "x"}),
			result: "error creating directory: mkdir " + blocker + ": not a directory",
			err:    "error: creating directory: mkdir " + blocker + ": not a directory", status: failed},
		{name: "write no path", call: toolCallFor("write_file", map[string]any{"content": "x"}), result: "error: path is required", err: "error: path is required", status: failed},
		{name: "skill ok", call: toolCallFor("read_skill", map[string]any{"name": "hello"}),
			result: "Skill \"hello\" (directory: /skills/hello)\n\nSay hello warmly.", status: ok},
		{name: "skill unknown", call: toolCallFor("read_skill", map[string]any{"name": "nope"}),
			result: `error: unknown skill "nope"`, err: `error: unknown skill "nope"`, status: failed},
		{name: "skill no name", call: toolCallFor("read_skill", map[string]any{}), result: "error: name is required", err: "error: name is required", status: failed},
		{name: "fetch ok", call: toolCallFor("fetch_url", map[string]any{"url": page.URL}),
			result: `{"content":"Listing Price: $20 million","status":200,"title":"Listing","url":"` + page.URL + `"}`, status: ok, http: 200},
		{name: "fetch 404", call: toolCallFor("fetch_url", map[string]any{"url": page.URL + "/missing"}),
			result: "error fetching URL: HTTP 404", err: "error: fetching URL: HTTP 404", status: failed, http: 404},
		{name: "fetch refused", call: toolCallFor("fetch_url", map[string]any{"url": closed.URL}),
			result: `error fetching URL: Get "` + closed.URL + `": dial tcp `, prefix: true, status: failed},
		{name: "fetch bad url", call: toolCallFor("fetch_url", map[string]any{"url": "file:///etc/hosts"}),
			result: "error: url must be an absolute HTTP or HTTPS URL", err: "error: url must be an absolute HTTP or HTTPS URL", status: failed},
		{name: "search ok", call: toolCallFor("web_search", map[string]any{"query": "found"}),
			result: `{"query":"found","results":[{"title":"A title","url":"https://example.com/a","snippet":"A snippet."}]}`, status: ok, http: 200},
		{name: "search empty", call: toolCallFor("web_search", map[string]any{"query": "nothing"}),
			result: `{"query":"nothing","results":[],"message":"No search results found."}`, status: ok, http: 200},
		{name: "search 500", call: toolCallFor("web_search", map[string]any{"query": "broken"}),
			result: "error searching web: HTTP 500", err: "error: searching web: HTTP 500", status: failed, http: 500},
		{name: "search no query", call: toolCallFor("web_search", map[string]any{"query": " "}),
			result: "error: query is required", err: "error: query is required", status: failed},
	}
	ctx := searchendpoint.With(context.Background(), search.URL)
	for _, tc := range cases {
		h := &hookRecorder{}
		sess := h.session()
		sess.catalog = catalog
		got := sess.execute(ctx, dir, tc.call)
		if tc.prefix && !strings.HasPrefix(got, tc.result) || !tc.prefix && got != tc.result {
			t.Errorf("%s: result = %q, want %q", tc.name, got, tc.result)
		}
		if len(h.ends) != 1 {
			t.Fatalf("%s: %d end hooks", tc.name, len(h.ends))
		}
		end := h.ends[0]
		if end.Preview != toolResultPreview(got) {
			t.Errorf("%s: preview = %q, want %q", tc.name, end.Preview, toolResultPreview(got))
		}
		wantErr := tc.err
		if tc.prefix && tc.status == failed {
			wantErr = "error: " + strings.TrimPrefix(got, "error ")
		}
		if end.Err != wantErr {
			t.Errorf("%s: err = %q, want %q", tc.name, end.Err, wantErr)
		}
		o := end.Outcome
		if o == nil {
			t.Errorf("%s: no outcome", tc.name)
			continue
		}
		if o.Status != tc.status || o.OutputPreview != end.Preview || o.InputSummary != h.starts[0].Summary {
			t.Errorf("%s: outcome = %+v, want %s with the call's preview and summary", tc.name, o, tc.status)
		}
		gotHTTP := 0
		if o.HTTPStatus != nil {
			gotHTTP = *o.HTTPStatus
		}
		if gotHTTP != tc.http {
			t.Errorf("%s: http status = %d, want %d", tc.name, gotHTTP, tc.http)
		}
	}
}

// A search whose context the job cancelled counts as cancelled, with the
// text the model reads unchanged.
func TestCancelledWebSearchIsCancelled(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	h := &hookRecorder{}
	sess := h.session()
	sess.onStart = func(c ToolCall) { h.starts = append(h.starts, c); cancel() }
	got := sess.execute(searchendpoint.With(ctx, server.URL), t.TempDir(), toolCallFor("web_search", map[string]any{"query": "slow"}))
	if !strings.HasPrefix(got, "error searching web: ") || !strings.Contains(got, "context canceled") {
		t.Fatalf("result = %q", got)
	}
	if o := h.ends[0].Outcome; o == nil || o.Status != events.ToolOutcomeCanceled || h.ends[0].Err != "error: "+strings.TrimPrefix(got, "error ") {
		t.Fatalf("end = %+v", h.ends[0])
	}
}

func TestWebSearchResultsKeepTheirText(t *testing.T) {
	t.Parallel()
	server := searchServer(t)
	for query, want := range map[string]string{
		"found":   `{"query":"found","results":[{"title":"A title","url":"https://example.com/a","snippet":"A snippet."}]}`,
		"nothing": `{"query":"nothing","results":[],"message":"No search results found."}`,
		"broken":  "error searching web: HTTP 500",
		"":        "error: query is required",
	} {
		if got, _ := toolWebSearchWithClient(context.Background(), map[string]any{"query": query}, server.Client(), server.URL); got != want {
			t.Errorf("%q: result = %q, want %q", query, got, want)
		}
	}
}

// view_image reports what it saw: a picture attached, or a model that cannot
// view pictures.
func TestViewImageReportsItsOutcome(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, tc := range []struct {
		name   string
		vision bool
		result string
		status events.ToolOutcomeStatus
		err    string
	}{
		{name: "attached", vision: true, result: "is now attached", status: events.ToolOutcomeSucceeded},
		{name: "cannot view", vision: false, result: cannotViewImages, status: events.ToolOutcomeFailed, err: cannotViewImages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, sent := toolModel(t, tc.vision, opensThenAnswers("shot.png"))
			writePNG(t, filepath.Join(o.workDir, "shot.png"))
			var ends []ToolResult
			opts := directTools
			opts.OnToolEnd = func(r ToolResult) { ends = append(ends, r) }
			if _, err := o.ThinkFull(t.Context(), "Look at shot.png.", opts); err != nil {
				t.Fatal(err)
			}
			result := strings.TrimPrefix(toolResult(t, sent()[1], "view_image"), "[tool:view_image]\n")
			if !strings.Contains(result, tc.result) {
				t.Fatalf("result = %q", result)
			}
			if len(ends) != 1 || ends[0].Outcome == nil || ends[0].Outcome.Status != tc.status || ends[0].Err != tc.err ||
				ends[0].Preview != toolResultPreview(result) || ends[0].Outcome.InputSummary != "shot.png" {
				t.Fatalf("ends = %+v", ends)
			}
		})
	}
}
