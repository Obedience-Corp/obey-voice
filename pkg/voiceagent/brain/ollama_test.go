package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
)

func drainStream(t *testing.T, stream *Stream) (chunks, thinking string) {
	t.Helper()
	got, think := collectStreamChans(t, stream)
	return strings.Join(got, ""), strings.Join(think, "")
}

func collectStreamChans(t *testing.T, stream *Stream) (chunks, thinking []string) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if stream.Thinking == nil {
			return
		}
		for s := range stream.Thinking {
			thinking = append(thinking, s)
		}
	}()
	for s := range stream.Chunks {
		chunks = append(chunks, s)
	}
	wg.Wait()
	return chunks, thinking
}

func TestModelRejectedTools(t *testing.T) {
	for msg, want := range map[string]bool{
		"400 Bad Request: registry.ollama.ai/library/kimi does not support tools": true,
		"model does not support tools":                                            true,
		"connection refused":                                                      false,
		"some other 400":                                                          false,
	} {
		if got := modelRejectedTools(errors.New(msg)); got != want {
			t.Errorf("modelRejectedTools(%q) = %v, want %v", msg, got, want)
		}
	}
}

// ollamaStub serves the Ollama chat API, rejecting requests that carry tools so
// we can exercise the retry-without-tools path.
func ollamaStub(t *testing.T, withTools, withoutTools *int) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools":`)) {
			*withTools++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"model does not support tools"}`)
			return
		}
		*withoutTools++
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

// ollamaStreamStub serves the Ollama chat API as NDJSON, one response line per
// entry, so streaming-delta behavior can be exercised without a real server.
func ollamaStreamStub(t *testing.T, lines []string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, l := range lines {
			_, _ = io.WriteString(w, l+"\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func chatCapturingStub(t *testing.T, into *map[string]any, think *any) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Options map[string]any `json:"options"`
			Think   any            `json:"think"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("stub: bad request body: %v", err)
		}
		// JSON numbers arrive as float64; compare as int so the test reads in
		// the units the config is written in.
		norm := map[string]any{}
		for k, v := range req.Options {
			if f, ok := v.(float64); ok {
				norm[k] = int(f)
				continue
			}
			norm[k] = v
		}
		*into = norm
		if think != nil {
			*think = req.Think
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

// TestThinkStreamStreamsDeltasWithToolsEnabled pins the fix: with tools enabled
// but no tool call this turn, each content delta must stream through as its own
// chunk rather than being buffered and sent as one combined chunk.
func TestThinkStreamStreamsDeltasWithToolsEnabled(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"lo"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":" world."},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}

	got, _ := collectStreamChans(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}

	want := []string{"Hel", "lo", " world."}
	if len(got) != len(want) {
		t.Fatalf("streamed chunks = %v, want %v (tools-enabled path must stream, not buffer)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Qwen3 dumps chain-of-thought into the reply body. Chunks must not carry the
// tags — they are speakable content only — and the assistant turn recorded in
// history must not either, or the model reads its own reasoning back on every
// following turn. Inner think text goes on the Thinking channel.
func TestThinkStreamKeepsThinkBlocksOutOfHistory(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"<think>The user said hi. "},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"Plan a warm greeting.</think>"},"done":false}`,
		`{"model":"m","message":{"role":"assistant","content":"\nHello there!"},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	deltas, thinking := drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}

	if len(o.history) == 0 {
		t.Fatal("no assistant turn recorded")
	}
	recorded := o.history[len(o.history)-1].Content
	for _, leak := range []string{"<think", "</think", "Plan a warm greeting", "The user said hi"} {
		if strings.Contains(recorded, leak) {
			t.Errorf("history carries %q: %q", leak, recorded)
		}
	}
	if !strings.Contains(recorded, "Hello there!") {
		t.Fatalf("history lost the reply: %q", recorded)
	}
	if strings.Contains(deltas, "<think>") || strings.Contains(deltas, "</think>") {
		t.Fatalf("chunks = %q, want think tags stripped off the speech bus", deltas)
	}
	if !strings.Contains(deltas, "Hello there!") {
		t.Fatalf("chunks lost the reply: %q", deltas)
	}
	if !strings.Contains(thinking, "The user said hi") || !strings.Contains(thinking, "Plan a warm greeting") {
		t.Fatalf("thinking = %q, want inner think text", thinking)
	}
	if strings.Contains(thinking, "<think>") || strings.Contains(thinking, "</think>") {
		t.Fatalf("thinking carries tags: %q", thinking)
	}
}

func TestThinkStreamThinkingFieldIsIncremental(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","thinking":"The user ","content":""},"done":false}`,
		`{"model":"m","message":{"role":"assistant","thinking":"said hi.","content":"Hello there!"},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	chunks, thinking := collectStreamChans(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if len(thinking) != 2 || thinking[0] != "The user " || thinking[1] != "said hi." {
		t.Fatalf("thinking chunks = %q, want incremental deltas not a cumulative second", thinking)
	}
	if strings.Join(chunks, "") != "Hello there!" {
		t.Fatalf("chunks = %q, want the greeting", chunks)
	}
	recorded := o.history[len(o.history)-1].Content
	if strings.Contains(recorded, "The user") || strings.Contains(recorded, "said hi") {
		t.Fatalf("history carries thinking-field body: %q", recorded)
	}
}

func TestThinkStreamPrefersThinkingFieldOverTaggedContent(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","thinking":"field body","content":"<think>tagged inner</think>Hello there!"},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	chunks, thinking := drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if thinking != "field body" {
		t.Fatalf("thinking = %q, want the field once, not tagged inner", thinking)
	}
	if strings.Contains(thinking, "tagged inner") {
		t.Fatalf("thinking duplicated FeedSplit inner text: %q", thinking)
	}
	if !strings.Contains(chunks, "Hello there!") {
		t.Fatalf("chunks = %q, want speakable content", chunks)
	}
	if strings.Contains(chunks, "<think>") {
		t.Fatalf("chunks carried tags: %q", chunks)
	}
}

func TestThinkStreamThinkingOnlyEmptyDoesNotFallback(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","thinking":"The user said hi. I should greet them.","content":""},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{client: ollamaStreamStub(t, lines), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	chunks, thinking := drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if !strings.Contains(thinking, "The user said hi") {
		t.Fatalf("thinking = %q, want the field", thinking)
	}
	if strings.Contains(chunks, fallbackResponse) || strings.Contains(chunks, oldRecoveryReply) {
		t.Fatalf("thinking-only empty streamed fallback %q", chunks)
	}
	if chunks != "" {
		t.Fatalf("chunks = %q, want empty for thinking-only", chunks)
	}
	if got := o.history[len(o.history)-1].Content; got != "" {
		t.Fatalf("history = %q, want empty thinking-only reply", got)
	}
}

func TestThinkStreamLiveRouterWebSearchSpawns(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"web_search","arguments":{"query":"latest news"}}}]},"done":true}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(t.Context(), "search the web", StreamOptions{
		ToolsEnabled:      true,
		RouterToolsPolicy: RouterToolsRouteOnly,
	})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	_, _ = drainStream(t, stream)
	res := <-stream.Done
	if res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if res.Route != "spawn" {
		t.Fatalf("Route = %q, want spawn", res.Route)
	}
	if reqs != 1 {
		t.Fatalf("requests = %d, want 1 (must not loop the rejected tool)", reqs)
	}
	if len(o.history) != 0 {
		t.Fatalf("live history = %#v, want empty so work does not occupy the talking window", o.history)
	}
}

func TestThinkStreamLiveRouterRouteSpawn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"route","arguments":{"kind":"spawn"}}}]},"done":true}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(t.Context(), "create a file", StreamOptions{
		ToolsEnabled:      true,
		RouterToolsPolicy: RouterToolsRouteOnly,
	})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	_, _ = drainStream(t, stream)
	res := <-stream.Done
	if res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if res.Route != "spawn" {
		t.Fatalf("Route = %q, want spawn", res.Route)
	}
	if len(o.history) != 0 {
		t.Fatalf("live history = %#v, want empty so work does not occupy the talking window", o.history)
	}
}

func TestThinkStreamThinkingThenToolsEmptyFallsBack(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","thinking":"need the files","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","thinking":"still nothing","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	chunks, _ := drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if !strings.Contains(chunks, fallbackResponse) {
		t.Fatalf("streamed %q, want fallback after thinking+tools+empty", chunks)
	}
}

func TestPreferOllamaThink(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"qwen3:4b", true},
		{"qwen3:30b", true},
		{"qwen3.8-uncensored:27b", true},
		{"Qwen3:4b", true},
		{" llama3.2", false},
		{"gemma3", false},
		{"", false},
		{"llama3.2", false},
	}
	for _, tt := range tests {
		if got := PreferOllamaThink(tt.model); got != tt.want {
			t.Errorf("PreferOllamaThink(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestResolveOllamaThink(t *testing.T) {
	tests := []struct {
		raw, model string
		want       any
	}{
		{"", "m", false},
		{"auto", "m", false},
		{"", "qwen3:4b", false},
		{"auto", "qwen3:4b", false},
		{"false", "qwen3:4b", false},
		{"true", "llama3.2", true},
		{"low", "m", "low"},
	}
	for _, tt := range tests {
		got := resolveOllamaThink(tt.raw, tt.model)
		if got == nil || got.Value != tt.want {
			t.Errorf("resolveOllamaThink(%q, %q) = %#v, want %#v", tt.raw, tt.model, got, tt.want)
		}
	}
	if got := ollamaThinkValue("auto"); got == nil || got.Value != false {
		t.Errorf("ollamaThinkValue(auto) = %#v, want false", got)
	}
}

func TestChatRequestQwen3AutoDoesNotForceThink(t *testing.T) {
	o := &OllamaBrain{model: "qwen3:4b", cfg: &config.Config{OllamaThink: "auto"}}
	req := o.newChatRequest(nil, nil)
	if req.Think == nil || req.Think.Value != false {
		t.Fatalf("Think = %#v, want false for qwen3+auto", req.Think)
	}
	o = &OllamaBrain{model: "qwen3:4b", cfg: &config.Config{}}
	req = o.newChatRequest(nil, nil)
	if req.Think == nil || req.Think.Value != false {
		t.Fatalf("Think = %#v, want false for qwen3+empty", req.Think)
	}
	o = &OllamaBrain{model: "qwen3:4b", cfg: &config.Config{OllamaThink: "true"}}
	req = o.newChatRequest(nil, nil)
	if req.Think == nil || req.Think.Value != true {
		t.Fatalf("Think = %#v, want true when explicitly set", req.Think)
	}
}

// A tool-calling turn records the model's preamble verbatim — that append never
// reaches cleanForVoice, so it is the one place a reasoning block survives into
// history on a qwen3 tool turn, and it is read back on every turn after.
func TestThinkStreamKeepsThinkBlocksOutOfToolCallHistory(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			// Reasoning preamble alongside the tool call, as qwen3 emits it.
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"<think>They want the files. Call list_files.</think>Let me look.","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"Here they are."},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	_, _ = drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}

	var preamble *api.Message
	for i := range o.history {
		if len(o.history[i].ToolCalls) > 0 {
			preamble = &o.history[i]
		}
	}
	if preamble == nil {
		t.Fatalf("no tool-calling assistant message in history: %+v", o.history)
	}
	for _, leak := range []string{"<think", "</think", "Call list_files"} {
		if strings.Contains(preamble.Content, leak) {
			t.Errorf("tool-call history carries %q: %q", leak, preamble.Content)
		}
	}
	if !strings.Contains(preamble.Content, "Let me look.") {
		t.Fatalf("tool-call history lost the preamble: %q", preamble.Content)
	}
}

// After a phantom tool call the model often restates the line it already
// streamed. Forwarding that copy made Veronica say the same sentence twice.
func TestThinkStreamDoesNotRestateAfterTools(t *testing.T) {
	const line = "Stay right there, baby."
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant","content":%q,"tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n", line)
			return
		}
		fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant","content":%q},"done":true,"done_reason":"stop"}`+"\n", line)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	var chunks []string
	for c := range stream.Chunks {
		chunks = append(chunks, c)
	}
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if reqs < 2 {
		t.Fatalf("expected a tool loop, got %d chat requests", reqs)
	}
	got := strings.Join(chunks, "")
	if got != line {
		t.Fatalf("streamed %q, want the spoken line once, not restated after the tool", got)
	}
}

func TestThinkStreamDoesNotRestateTaggedContentAfterTools(t *testing.T) {
	const line = "Stay right there, baby."
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant","content":%q,"tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n", line)
			return
		}
		fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant","content":"<think>plan a greeting</think>%s"},"done":true,"done_reason":"stop"}`+"\n", line)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	chunks, _ := drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if reqs < 2 {
		t.Fatalf("expected a tool loop, got %d chat requests", reqs)
	}
	if chunks != line {
		t.Fatalf("streamed %q, want the spoken line once (tagged restatement must not replay)", chunks)
	}
}

func TestRestatementSkip(t *testing.T) {
	const already = "Stay right there, baby."
	tests := []struct {
		soFar string
		skip  bool
	}{
		{"", false},
		{"Stay", true},
		{"Stay right there, baby.", true},
		{"Stay right there, baby. And then this.", false},
		{"Something else entirely.", false},
	}
	for _, tt := range tests {
		if got := restatementSkip(already, tt.soFar); got != tt.skip {
			t.Errorf("restatementSkip(%q, %q) = %v, want %v", already, tt.soFar, got, tt.skip)
		}
	}
	if restatementSkip("", "hello") {
		t.Error("empty already must not skip; that is the first iteration")
	}
	if !restatementSkip(already, "<think>plan a greeting</think>"+already) {
		t.Error("tagged restatement of the same reply must skip")
	}
}

// Tool-only turns used to close the stream with zero chunks, so the TUI showed
// tool activity and then nothing — "looking into it" with no reply.
func TestThinkStreamFallbackAfterToolOnlyTurn(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			// First response: tool call, no text.
			line := `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`
			_, _ = io.WriteString(w, line+"\n")
			return
		}
		// Second response after tool results: empty content again.
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}

	var chunks []string
	for c := range stream.Chunks {
		chunks = append(chunks, c)
	}
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}
	if reqs < 2 {
		t.Fatalf("expected tool loop to re-request model, got %d chat requests", reqs)
	}
	joined := strings.Join(chunks, "")
	if joined != fallbackResponse {
		t.Fatalf("streamed %q, want fallback %q after empty tool-only turn", joined, fallbackResponse)
	}
	if len(o.history) == 0 || o.history[len(o.history)-1].Content != fallbackResponse {
		t.Fatalf("history tail = %+v, want fallback assistant message", o.history)
	}
}

func TestModelRejectedThink(t *testing.T) {
	for msg, want := range map[string]bool{
		"model does not support thinking":      true,
		"400: llama3.2 does not support think": true,
		"does not support tools":               false,
		"connection refused":                   false,
	} {
		if got := modelRejectedThink(errors.New(msg)); got != want {
			t.Errorf("modelRejectedThink(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestChatRetriesWithoutThinkWhenUnsupported(t *testing.T) {
	var withThink, withoutThink int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"think":true`)) {
			withThink++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"model does not support thinking"}`)
			return
		}
		withoutThink++
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m"}
	stream := false
	req := &api.ChatRequest{
		Model:    "m",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Stream:   &stream,
		Think:    &api.ThinkValue{Value: true},
	}
	if err := o.chat(context.Background(), req, func(api.ChatResponse) error { return nil }); err != nil {
		t.Fatalf("chat returned error: %v", err)
	}
	if withThink != 1 || withoutThink != 1 {
		t.Errorf("want 1 think-call then 1 retry without; got withThink=%d withoutThink=%d", withThink, withoutThink)
	}
	if req.Think == nil || req.Think.Value != false {
		t.Errorf("Think = %#v, want false after the reject retry", req.Think)
	}
}

func TestChatDoesNotRetryThinkOnUnrelatedError(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"connection refused"}`)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{client: api.NewClient(base, http.DefaultClient), model: "m"}
	stream := false
	req := &api.ChatRequest{
		Model:    "m",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Stream:   &stream,
		Think:    &api.ThinkValue{Value: true},
	}
	if err := o.chat(context.Background(), req, func(api.ChatResponse) error { return nil }); err == nil {
		t.Fatal("chat returned nil, want the unrelated error")
	}
	if n != 1 {
		t.Errorf("requests = %d, want 1 (non-think errors must not retry without Think)", n)
	}
	if req.Think == nil || req.Think.Value != true {
		t.Errorf("Think = %#v, want still true (no think-reject retry)", req.Think)
	}
}

func TestChatRetriesWithoutToolsWhenUnsupported(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{client: ollamaStub(t, &withTools, &withoutTools), model: "m"}

	stream := false
	req := &api.ChatRequest{
		Model:    "m",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
		Tools:    voiceAssistantTools(nil),
		Stream:   &stream,
	}

	var got api.Message
	if err := o.chat(context.Background(), req, func(resp api.ChatResponse) error {
		got = resp.Message
		return nil
	}); err != nil {
		t.Fatalf("chat returned error: %v", err)
	}

	if got.Content != "hi" {
		t.Errorf("content = %q, want %q", got.Content, "hi")
	}
	if withTools != 1 || withoutTools != 1 {
		t.Errorf("want 1 tool-call then 1 retry without; got withTools=%d withoutTools=%d", withTools, withoutTools)
	}
	if req.Tools != nil {
		t.Error("tools should be cleared after the unsupported-tools retry")
	}
}

// recordingStub captures each request body so a test can assert what was sent.
func recordingStub(t *testing.T, count *int, bodies *[][]byte) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*count++
		body, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, body)
		var req api.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		if hasTool(req.Tools, "route") {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestWarmupFiresMinimalRequest(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m"}

	o.Warmup(context.Background())

	if count != 1 {
		t.Fatalf("want exactly 1 warmup request, got %d", count)
	}
	body := bodies[0]
	if bytes.Contains(body, []byte(`"tools":`)) {
		t.Errorf("warmup without a config must not send tools; body=%s", body)
	}
	if !bytes.Contains(body, []byte(`"num_predict":1`)) {
		t.Errorf("warmup must cap generation with num_predict; body=%s", body)
	}
	if !bytes.Contains(body, []byte(`"num_ctx":8192`)) {
		t.Errorf("warmup must pin a safe num_ctx, not the model default; body=%s", body)
	}
}

// TestWarmupPrimesTheFirstTurnPrefix: the warmup request must carry the same
// system prompt and the same tool schema as the first live turn, in the same
// bytes, or the prompt cache it fills is never reused. On qwen3.8 that miss is
// a ~5 s first sentence.
func TestWarmupPrimesTheFirstTurnPrefix(t *testing.T) {
	var count int
	var bodies [][]byte
	cfg := &config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128, VoiceToolsEnabled: true, VoiceRouterTools: "route_only", AgentName: "Samantha", MaxHistory: 10}
	o := &OllamaBrain{
		client:           recordingStub(t, &count, &bodies),
		model:            "m",
		cfg:              cfg,
		fullSystemPrompt: "You are Samantha. Keep it short.",
	}

	o.Warmup(context.Background())

	if count != 1 {
		t.Fatalf("want 1 warmup request, got %d", count)
	}
	var sent struct {
		Messages []api.Message   `json:"messages"`
		Tools    json.RawMessage `json:"tools"`
		Options  map[string]any  `json:"options"`
	}
	if err := json.Unmarshal(bodies[0], &sent); err != nil {
		t.Fatalf("decode warmup body: %v", err)
	}
	firstTurn := o.buildMessages("")
	if len(sent.Messages) < 2 || sent.Messages[0].Role != "system" || sent.Messages[0].Content != firstTurn[0].Content {
		t.Fatalf("warmup system message = %+v, want the frozen system prompt %q", sent.Messages, firstTurn[0].Content)
	}
	if sent.Messages[len(sent.Messages)-1].Role != "user" {
		t.Fatalf("warmup must end with a user message, got %+v", sent.Messages)
	}
	live := (&toolSession{catalog: o.skills, routerPolicy: RouterToolsRouteOnly}).tools()
	want, _ := json.Marshal(live)
	if string(sent.Tools) != string(want) {
		t.Fatalf("warmup tools = %s\nwant the live route schema %s", sent.Tools, want)
	}
	if sent.Options["num_predict"] != float64(1) {
		t.Fatalf("warmup must cap generation at one token, got %v", sent.Options["num_predict"])
	}
}

// Tools off in config means the live turn sends none, so warmup must not either.
func TestWarmupSendsNoToolsWhenVoiceToolsDisabled(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{
		client:           recordingStub(t, &count, &bodies),
		model:            "m",
		cfg:              &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: false},
		fullSystemPrompt: "You are Samantha.",
	}
	o.Warmup(context.Background())
	if bytes.Contains(bodies[0], []byte(`"tools":`)) {
		t.Fatalf("warmup sent tools with voice_tools_enabled=false; body=%s", bodies[0])
	}
}

// Warmup follows live compatibility handling. Required routing must not be
// dropped; the legacy chat fallback still preloads a text-only model.
func TestWarmupToolRejectionFollowsRoutingPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy RouterToolsPolicy
		chats  int
	}{{RouterToolsRouteOnly, 0}, {RouterToolsLegacy, 1}} {
		t.Run(string(tc.policy), func(t *testing.T) {
			var withTools, withoutTools int
			o := &OllamaBrain{
				client:           ollamaStub(t, &withTools, &withoutTools),
				model:            "m",
				cfg:              &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, VoiceRouterTools: string(tc.policy)},
				fullSystemPrompt: "You are Samantha.",
			}
			o.Warmup(context.Background())
			if withTools != 1 || withoutTools != tc.chats {
				t.Fatalf("requests: tools=%d chat=%d, want 1 / %d", withTools, withoutTools, tc.chats)
			}
		})
	}
}

// The schema follows the configured router policy and the delegation
// runtime, exactly as the live turn's does.
func TestWarmupToolsFollowThePolicyAndDelegation(t *testing.T) {
	runtime, err := delegation.New(context.Background(), &delegationClientStub{started: make(chan struct{})}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	tests := []struct {
		name   string
		policy string
		rt     *delegation.Runtime
		want   *toolSession
	}{
		{name: "legacy", policy: "legacy", want: &toolSession{routerPolicy: RouterToolsLegacy}},
		{name: "route_only with delegation", policy: "route_only", rt: runtime, want: &toolSession{routerPolicy: RouterToolsRouteOnly, delegation: runtime}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var count int
			var bodies [][]byte
			o := &OllamaBrain{
				client:           recordingStub(t, &count, &bodies),
				model:            "m",
				cfg:              &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, VoiceRouterTools: tt.policy},
				fullSystemPrompt: "You are Samantha.",
				delegation:       tt.rt,
			}
			o.Warmup(context.Background())
			var sent struct {
				Tools json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(bodies[0], &sent); err != nil {
				t.Fatalf("decode warmup body: %v", err)
			}
			want, _ := json.Marshal(tt.want.tools())
			if string(sent.Tools) != string(want) {
				t.Fatalf("warmup tools = %s\nwant %s", sent.Tools, want)
			}
		})
	}
}

// Warmup runs on its own goroutine while startup may still be restoring a
// session and the first turn may be rebuilding the prompt. Run under -race.
func TestWarmupDoesNotRaceTheConversationLoop(t *testing.T) {
	runtime, err := delegation.New(context.Background(), &delegationClientStub{started: make(chan struct{})}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"hi"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{
		client:           api.NewClient(base, http.DefaultClient),
		model:            "m",
		cfg:              &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true},
		fullSystemPrompt: "You are Samantha.",
		delegation:       runtime,
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.Warmup(context.Background())
		}()
	}
	for i := 0; i < 8; i++ {
		o.LoadHistory(nil)
		o.setFullSystemPrompt(fmt.Sprintf("You are Samantha, take %d.", i))
	}
	wg.Wait()
	o.retiredDelegations.Wait()
}

func TestWarmupUsesConfiguredNumCtx(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{
		client: recordingStub(t, &count, &bodies),
		model:  "kimi-k2.6-distilled:IQ4_XS",
		cfg:    &config.Config{OllamaNumCtx: 4096, OllamaThink: "false"},
	}

	o.Warmup(context.Background())

	if count != 1 {
		t.Fatalf("want 1 warmup request, got %d", count)
	}
	if !bytes.Contains(bodies[0], []byte(`"num_ctx":4096`)) {
		t.Fatalf("warmup num_ctx = %s, want 4096", bodies[0])
	}
}

func TestWarmupSkipsWhenContextCanceled(t *testing.T) {
	var count int
	var bodies [][]byte
	o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	o.Warmup(ctx)

	if count != 0 {
		t.Fatalf("canceled warmup must not hit Ollama, got %d requests", count)
	}
}

// TestSystemPrefixStableAcrossTurns guards the KV-cache reuse property: the
// system message (the cached prefix) must be byte-identical across turns, even
// as conversation history grows.
func TestSystemPrefixStableAcrossTurns(t *testing.T) {
	o := &OllamaBrain{
		workDir:          "/work/dir",
		cfg:              &config.Config{AgentName: "Samantha", MaxHistory: 10},
		fullSystemPrompt: "frozen system prompt",
	}

	o.history = append(o.history, api.Message{Role: "user", Content: "first"})
	firstPrefix := o.buildMessages("")[0].Content

	o.history = append(o.history,
		api.Message{Role: "assistant", Content: "hello there"},
		api.Message{Role: "user", Content: "second"},
	)
	// Skill activations must not touch the system message either — they
	// splice onto the tail.
	secondPrefix := o.buildMessages("\n<activated_skills>calibre</activated_skills>")[0].Content

	if firstPrefix != secondPrefix {
		t.Errorf("system prefix changed across turns, defeating KV-cache reuse:\nfirst=%q\nsecond=%q", firstPrefix, secondPrefix)
	}
}

// TestBuildMessagesSplicesSkillContextOntoLatestUserMessage guards the tail
// injection point: activated-skill instructions ride the newest user message,
// where they cannot invalidate the cached prefix, and the stored history
// stays clean for session persistence and later requests.
func TestBuildMessagesSplicesSkillContextOntoLatestUserMessage(t *testing.T) {
	o := &OllamaBrain{
		workDir:          "/work/dir",
		cfg:              &config.Config{MaxHistory: 10},
		fullSystemPrompt: "sys",
		history: []api.Message{
			{Role: "user", Content: "older"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "current"},
		},
	}
	msgs := o.buildMessages("\n<activated_skills>x</activated_skills>")

	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "<activated_skills>") {
		t.Fatalf("last message = %+v, want skill context on the latest user message", last)
	}
	if !strings.HasPrefix(last.Content, "current") {
		t.Fatalf("last message content = %q, want original input preserved first", last.Content)
	}
	if strings.Contains(msgs[0].Content, "<activated_skills>") {
		t.Fatal("skill context leaked into the system message")
	}
	if strings.Contains(msgs[1].Content, "<activated_skills>") {
		t.Fatal("skill context landed on an older user message")
	}
	if strings.Contains(o.history[2].Content, "<activated_skills>") {
		t.Fatal("splice mutated stored history; sessions would persist skill blocks")
	}
}

// toolGroupHistory is a two-turn conversation where each turn contains a
// tool-call group: user, assistant{ToolCalls}, tool results, final assistant.
func toolGroupHistory() []api.Message {
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{}}}
	return []api.Message{
		{Role: "user", Content: "u1"},      // 0
		call,                               // 1
		{Role: "tool", Content: "r1"},      // 2
		{Role: "tool", Content: "r2"},      // 3
		{Role: "assistant", Content: "a1"}, // 4
		{Role: "user", Content: "u2"},      // 5
		call,                               // 6
		{Role: "tool", Content: "r3"},      // 7
		{Role: "assistant", Content: "a2"}, // 8
	}
}

// assertNoStrandedTools fails if msgs starts with a tool result or contains a
// tool result whose assistant tool-call antecedent was trimmed away.
func assertNoStrandedTools(t *testing.T, msgs []api.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if i == 0 {
			t.Fatal("message window starts with role tool")
		}
		prev := msgs[i-1]
		if prev.Role == "tool" || (prev.Role == "assistant" && len(prev.ToolCalls) > 0) {
			continue
		}
		t.Fatalf("tool result at index %d lacks its assistant tool-call antecedent", i)
	}
}

func TestHistoryWindowStart(t *testing.T) {
	hist := toolGroupHistory()

	tests := []struct {
		name string
		max  int
		want int
	}{
		{"window opens on tool, no later user, drops tail group", 2, 9},
		{"window opens on tool result, advances to next user", 7, 5},
		{"window opens mid first tool group", 6, 5},
		{"window opens on user", 4, 5},
		{"window opens on assistant tool-call", 3, 5},
		{"window fits whole history", 9, 0},
		{"max larger than history", 20, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := historyWindowStart(hist, tt.max)
			if got != tt.want {
				t.Fatalf("historyWindowStart(max=%d) = %d, want %d", tt.max, got, tt.want)
			}
			assertNoStrandedTools(t, hist[got:])
		})
	}
}

func TestTrimmedHistoryNeverStrandsToolResults(t *testing.T) {
	// trimHistory owns the retention bound now (buildMessages passes retained
	// history through unchanged), so the stranding guard applies at trim time.
	for maxHistory := 1; maxHistory <= 5; maxHistory++ {
		o := &OllamaBrain{
			workDir: "/work/dir",
			cfg:     &config.Config{AgentName: "Samantha", MaxHistory: maxHistory},
			history: toolGroupHistory(),
		}
		o.trimHistory()
		msgs := o.buildMessages("")
		if msgs[0].Role != "system" {
			t.Fatalf("maxHistory=%d: first message role = %q, want system", maxHistory, msgs[0].Role)
		}
		assertNoStrandedTools(t, msgs[1:])
	}
}

func TestTrimHistoryWithNonPositiveMaxDropsHistory(t *testing.T) {
	for _, maxHistory := range []int{0, -1} {
		o := &OllamaBrain{
			workDir: "/work/dir",
			cfg:     &config.Config{AgentName: "Samantha", MaxHistory: maxHistory},
			history: toolGroupHistory(),
		}
		o.trimHistory()
		if len(o.history) != 0 {
			t.Fatalf("maxHistory=%d: retained %d messages, want 0", maxHistory, len(o.history))
		}
		if msgs := o.buildMessages(""); len(msgs) != 1 || msgs[0].Role != "system" {
			t.Fatalf("maxHistory=%d: messages = %+v, want system prompt only", maxHistory, msgs)
		}
	}
}

// TestTrimHistoryBlockTrim guards the high/low water contract: nothing is
// dropped until the count exceeds MaxHistory*2 (so the prefix — and the
// server's KV cache — stays byte-stable between trims), then one cut lands
// the count at or under MaxHistory.
func TestTrimHistoryBlockTrim(t *testing.T) {
	turns := func(n int) []api.Message {
		var h []api.Message
		for i := 0; i < n; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			h = append(h, api.Message{Role: role, Content: fmt.Sprintf("m%d", i)})
		}
		return h
	}

	o := &OllamaBrain{cfg: &config.Config{MaxHistory: 4}}

	o.history = turns(8) // exactly at high water: untouched
	o.trimHistory()
	if len(o.history) != 8 {
		t.Fatalf("at high water: retained %d, want 8 (no per-turn slide)", len(o.history))
	}
	if o.history[0].Content != "m0" {
		t.Fatalf("at high water: prefix shifted to %q", o.history[0].Content)
	}

	o.history = turns(9) // over high water: cut to low water
	o.trimHistory()
	if len(o.history) > 4 {
		t.Fatalf("over high water: retained %d, want <= MaxHistory (4)", len(o.history))
	}
	assertNoStrandedTools(t, o.history)
}

func TestTrimHistoryNeverStrandsToolResults(t *testing.T) {
	o := &OllamaBrain{
		cfg:     &config.Config{MaxHistory: 3},
		history: toolGroupHistory(),
	}
	o.trimHistory()
	if len(o.history) == 0 {
		t.Fatal("trimHistory dropped all history")
	}
	if o.history[0].Role == "tool" {
		t.Fatal("trimmed history starts with role tool")
	}
	assertNoStrandedTools(t, o.history)
}

func TestChatRequestCarriesContextPolicy(t *testing.T) {
	d := api.Duration{Duration: 10 * time.Minute}
	o := &OllamaBrain{
		model:     "m",
		cfg:       &config.Config{OllamaNumCtx: 8192},
		keepAlive: &d,
	}
	req := o.newChatRequest([]api.Message{{Role: "system", Content: "s"}}, nil)
	if got := req.Options["num_ctx"]; got != 8192 {
		t.Fatalf("num_ctx = %v, want 8192", got)
	}
	if req.KeepAlive == nil || req.KeepAlive.Duration != 10*time.Minute {
		t.Fatalf("KeepAlive = %v, want 10m", req.KeepAlive)
	}
	// Non-qwen3 model with empty ollama_think: Prefer is false, so Think is off.
	if req.Think == nil || req.Think.Value != false {
		t.Fatalf("Think = %#v, want false for non-qwen3", req.Think)
	}

	// num_ctx 0 defers to the server default: no Options at all.
	o = &OllamaBrain{model: "m", cfg: &config.Config{}}
	if req := o.newChatRequest(nil, nil); req.Options != nil {
		t.Fatalf("Options = %v, want nil when ollama_num_ctx and ollama_num_batch are 0", req.Options)
	}
}

// TestChatRequestCarriesNumBatch is the regression for B2: Ollama derives the
// prompt batch from the context window alone, so raising num_ctx to hold a
// conversation quietly halves prompt-reading throughput. On
// qwen3.8-uncensored:27b that was a 4.4 s pause before every spoken word.
// num_batch has to reach the request, and it has to be absent — not zero —
// when the setting is off, because an explicit 0 would be a real batch size to
// Ollama rather than "choose for me".
func TestChatRequestCarriesNumBatch(t *testing.T) {
	o := &OllamaBrain{model: "m", cfg: &config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128}}
	req := o.newChatRequest(nil, nil)
	if got := req.Options["num_batch"]; got != 128 {
		t.Fatalf("num_batch = %v, want 128", got)
	}
	if got := req.Options["num_ctx"]; got != 8192 {
		t.Fatalf("num_ctx = %v, want 8192 alongside num_batch", got)
	}

	// 0 means "let Ollama choose", which it can only do if the key is missing.
	o = &OllamaBrain{model: "m", cfg: &config.Config{OllamaNumCtx: 8192}}
	req = o.newChatRequest(nil, nil)
	if _, ok := req.Options["num_batch"]; ok {
		t.Fatalf("num_batch present (%v) when ollama_num_batch is 0; Ollama must pick its own", req.Options["num_batch"])
	}
}

// TestWarmupMatchesChatOptions guards a subtle way to lose the whole benefit:
// Ollama reloads the model when a request's options differ from the loaded
// runner's, so a warmup that omits num_batch warms a runner the first real turn
// throws away — and the user pays a model load in the middle of saying hello.
func TestWarmupMatchesChatOptions(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		diverge string // non-empty: the key that is knowingly allowed to differ
		why     string
	}{
		{name: "configured", cfg: config.Config{OllamaNumCtx: 8192, OllamaNumBatch: 128}},
		{name: "batch off", cfg: config.Config{OllamaNumCtx: 8192}},
		{name: "small window", cfg: config.Config{OllamaNumCtx: 2048, OllamaNumBatch: 64}},
		{name: "large window", cfg: config.Config{OllamaNumCtx: 32768, OllamaNumBatch: 256}},
		{
			// A known divergence, pinned here rather than left to be discovered
			// in a log. ollama_num_ctx 0 means "let the server size it", so the
			// chat request omits num_ctx — but Warmup substitutes
			// defaultWarmupNumCtx to avoid loading the model at its advertised
			// 262144 window. The two disagree, so the first real turn reloads
			// the model, and on these tags it reloads at 262144. Reconciling
			// them is a behaviour change to a documented setting and does not
			// belong in a latency fix; this test exists so the next person
			// finds it stated instead of inferring it from a stall.
			name:    "window unset diverges",
			cfg:     config.Config{OllamaNumBatch: 128},
			diverge: "num_ctx",
			why:     "warmup pins defaultWarmupNumCtx while chat defers to the server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			var gotThink any
			cfg := tt.cfg
			o := &OllamaBrain{model: "m", cfg: &cfg, client: chatCapturingStub(t, &got, &gotThink)}
			o.Warmup(context.Background())
			chat := o.newChatRequest(nil, nil)

			for _, k := range []string{"num_ctx", "num_batch"} {
				w, c := got[k], chat.Options[k]
				if k == tt.diverge {
					if w == c {
						t.Fatalf("%s now agrees (%v): the documented divergence is gone, so delete this case", k, w)
					}
					t.Logf("known divergence: warmup %s = %v, chat %s = %v (%s)", k, w, k, c, tt.why)
					continue
				}
				if w != c {
					t.Fatalf("warmup %s = %v, chat %s = %v: a mismatch reloads the model on the first real turn", k, w, k, c)
				}
			}
			if chat.Think == nil {
				t.Fatal("chat Think is nil")
			}
			if gotThink != chat.Think.Value {
				t.Fatalf("warmup Think = %#v, chat Think = %#v: a mismatch reloads the model on the first real turn", gotThink, chat.Think.Value)
			}
		})
	}
}

// TestChatRequestThinkPolicy: empty/"auto" on a non-qwen3 model stay
// Think=false (PreferOllamaThink is false). Explicit levels still map through.
func TestChatRequestThinkPolicy(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want any
	}{
		{"default empty", "", false},
		{"auto", "auto", false},
		{"false", "false", false},
		{"off", "off", false},
		{"true", "true", true},
		{"level low", "low", "low"},
		{"level high", "HIGH", "high"},
		{"invalid falls back off", "banana", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &OllamaBrain{model: "m", cfg: &config.Config{OllamaThink: tt.cfg}}
			req := o.newChatRequest(nil, nil)
			if req.Think == nil {
				t.Fatal("Think is nil, want a value so Ollama does not use model default thinking")
			}
			if got := req.Think.Value; got != tt.want {
				t.Fatalf("Think.Value = %#v (%T), want %#v", got, got, tt.want)
			}
		})
	}
}

func TestOllamaThinkValue(t *testing.T) {
	if got := ollamaThinkValue("medium"); got == nil || got.Value != "medium" {
		t.Fatalf("medium = %#v", got)
	}
	if got := ollamaThinkValue("FALSE"); got == nil || got.Value != false {
		t.Fatalf("FALSE = %#v", got)
	}
}

// TestEnsureContextBudgetShrinksHistory guards the explicit-budget contract:
// when the estimated prompt would overflow ollama_num_ctx, the oldest history
// is dropped (never the system prompt) instead of letting the server truncate
// from the top.
func TestEnsureContextBudgetShrinksHistory(t *testing.T) {
	long := strings.Repeat("x", 2000) // ~500 estimated tokens per message
	var hist []api.Message
	for i := 0; i < 10; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		hist = append(hist, api.Message{Role: role, Content: long})
	}
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 1000},
		fullSystemPrompt: "system prompt stays",
		history:          hist,
	}

	o.ensureContextBudget("")
	if len(o.history) >= 10 {
		t.Fatalf("history not shrunk: %d messages retained over a 1000-token budget", len(o.history))
	}
	if len(o.history) < 2 {
		t.Fatalf("budget shrink dropped the in-flight turn: %d messages", len(o.history))
	}
	if o.fullSystemPrompt != "system prompt stays" {
		t.Fatal("budget shrink touched the system prompt")
	}

	// Within budget: untouched.
	o = &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 100000},
		fullSystemPrompt: "s",
		history:          toolGroupHistory(),
	}
	before := len(o.history)
	o.ensureContextBudget("")
	if len(o.history) != before {
		t.Fatalf("budget shrink ran under budget: %d -> %d", before, len(o.history))
	}
}

// Session 20260831-152533-026a died mid-tool-loop with Ollama 500
// "no user query found in messages": large camp/festival dumps overflowed
// num_ctx and the server dropped the user turn. The in-flight query must stay.
func TestEnsureContextBudgetPinsLatestUserDuringToolLoop(t *testing.T) {
	huge := strings.Repeat("x", 8000)
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command"}}}}
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 400},
		fullSystemPrompt: "sys",
		history: []api.Message{
			{Role: "user", Content: "older question"},
			{Role: "assistant", Content: "older reply"},
			{Role: "user", Content: "summarize my campaign"},
			call,
			{Role: "tool", Content: huge, ToolName: "run_command"},
			{Role: "tool", Content: huge, ToolName: "run_command"},
		},
	}

	o.ensureContextBudget("")
	if idx := lastUserIndex(o.history); idx < 0 || o.history[idx].Content != "summarize my campaign" {
		t.Fatalf("lost in-flight user query: %+v", rolesOf(o.history))
	}
	assertNoStrandedTools(t, o.history)
	assertWithinContextBudget(t, o, "")
}

func TestEnsureContextBudgetShrinksToolResultsToKeepUser(t *testing.T) {
	huge := strings.Repeat("campaign yaml dump ", 400)
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command"}}}}
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 400},
		fullSystemPrompt: "sys",
		history: []api.Message{
			{Role: "user", Content: "summarize my campaign"},
			call,
			{Role: "tool", Content: huge, ToolName: "run_command"},
			{Role: "tool", Content: huge, ToolName: "run_command"},
		},
	}

	o.ensureContextBudget("")
	if got := lastUserIndex(o.history); got != 0 {
		t.Fatalf("last user index = %d, want 0 (cannot drop the only query)", got)
	}
	assertWithinContextBudget(t, o, "")
	if newest, oldest := o.history[3].Content, o.history[2].Content; len(newest) < len(oldest) {
		t.Fatalf("newest tool (%d bytes) smaller than oldest (%d); budget should keep the latest result", len(newest), len(oldest))
	}
}

func TestEnsureContextBudgetShrinksSharedToolAndKeepsCurrentUser(t *testing.T) {
	const prefix = "Santa tool (lookup):\n"
	shared := prefix + strings.Repeat("foreign tool evidence ", 400)
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 400},
		fullSystemPrompt: "sys",
		history: []api.Message{
			{Role: "user", Content: "current real user request"},
			{Role: "shared_tool", Content: shared},
		},
	}

	o.ensureContextBudget("")

	if idx := lastUserIndex(o.history); idx != 0 || o.history[idx].Content != "current real user request" {
		t.Fatalf("current real user = index %d, history=%+v; want preserved user at index 0", idx, o.history)
	}
	if len(o.history[1].Content) >= len(shared) {
		t.Fatalf("shared tool was not shrunk: %d bytes, want less than %d", len(o.history[1].Content), len(shared))
	}
	if !strings.HasPrefix(o.history[1].Content, prefix) {
		t.Fatalf("shared tool attribution prefix = %q, want %q", o.history[1].Content, prefix)
	}
	assertWithinContextBudget(t, o, "")
}

func TestEnsureContextBudgetFitsSmallNumCtx(t *testing.T) {
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command"}}}}
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 128},
		fullSystemPrompt: "sys",
		history: []api.Message{
			{Role: "user", Content: "summarize my campaign"},
			call,
			{Role: "tool", Content: strings.Repeat("x", 8000), ToolName: "run_command"},
		},
	}

	o.ensureContextBudget("")
	if o.history[0].Role != "user" {
		t.Fatalf("lost user query: %+v", rolesOf(o.history))
	}
	assertWithinContextBudget(t, o, "")
}

func TestEnsureContextBudgetFitsManyToolResults(t *testing.T) {
	call := api.Message{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "run_command"}}}}
	hist := []api.Message{
		{Role: "user", Content: "summarize my campaign"},
		call,
	}
	for range 8 {
		hist = append(hist, api.Message{Role: "tool", Content: strings.Repeat("x", 4000), ToolName: "run_command"})
	}
	o := &OllamaBrain{
		cfg:              &config.Config{MaxHistory: 20, OllamaNumCtx: 400},
		fullSystemPrompt: "sys",
		history:          hist,
	}

	o.ensureContextBudget("")
	if o.history[0].Content != "summarize my campaign" {
		t.Fatalf("lost user query: %+v", rolesOf(o.history))
	}
	assertWithinContextBudget(t, o, "")
}

func assertWithinContextBudget(t *testing.T, o *OllamaBrain, skillCtx string) {
	t.Helper()
	limit := o.cfg.OllamaNumCtx * 9 / 10
	if got := o.estimateTokens(skillCtx); got > limit {
		t.Fatalf("estimateTokens = %d, want <= %d (num_ctx=%d)", got, limit, o.cfg.OllamaNumCtx)
	}
}

func TestThinkStreamToolResultsCarryToolName(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	stream, err := o.ThinkStream(t.Context(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	_, _ = drainStream(t, stream)
	if res := <-stream.Done; res.Err != nil {
		t.Fatalf("stream done error = %v", res.Err)
	}

	found := false
	for _, m := range o.history {
		if m.Role == "tool" {
			found = true
			if m.ToolName != "list_files" {
				t.Fatalf("tool message ToolName = %q, want list_files", m.ToolName)
			}
		}
	}
	if !found {
		t.Fatal("no tool result in history")
	}
}

func rolesOf(history []api.Message) []string {
	roles := make([]string, len(history))
	for i, m := range history {
		roles[i] = m.Role
	}
	return roles
}

// TestThinkFullSpeaksFallbackVerbatim: an empty model response must yield the
// canned fallback untouched — cleanForVoice runs before the fallback is
// substituted, never on it.
func TestThinkFullSpeaksFallbackVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp != fallbackResponse {
		t.Errorf("resp = %q, want fallback verbatim %q", resp, fallbackResponse)
	}
}

func TestThinkFullThinkingOnlyEmptyDoesNotFallback(t *testing.T) {
	const thought = "The user said hi. I should greet them."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"model":"m","message":{"role":"assistant","thinking":%q,"content":""},"done":true,"done_reason":"stop"}`+"\n", thought)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp != "" {
		t.Fatalf("resp = %q, want empty thinking-only (not fallback)", resp)
	}
	if len(o.history) == 0 {
		t.Fatal("no assistant turn recorded")
	}
	tail := o.history[len(o.history)-1]
	if tail.Content != "" {
		t.Errorf("history Content = %q, want empty", tail.Content)
	}
	if strings.Contains(tail.Content, thought) || tail.Thinking != "" {
		t.Errorf("history persisted thinking: Content=%q Thinking=%q", tail.Content, tail.Thinking)
	}
}

func TestThinkFullToolOnlyEmptyFallsBack(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp != fallbackResponse {
		t.Fatalf("resp = %q, want fallback after tool-only empty", resp)
	}
}

func TestThinkFullThinkingThenToolsEmptyFallsBack(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 1 {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","thinking":"need the files","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"."}}}]},"done":true}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","thinking":"still nothing","content":""},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)

	o := &OllamaBrain{
		client:  api.NewClient(base, http.DefaultClient),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "list files", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp != fallbackResponse {
		t.Fatalf("resp = %q, want fallback after thinking+tools+empty", resp)
	}
}

func TestThinkFullOmitsToolsWhenDisabled(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{
		client:  ollamaStub(t, &withTools, &withoutTools),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	resp, err := o.ThinkFull(context.Background(), "hello", StreamOptions{ToolsEnabled: false})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if resp == "" {
		t.Fatal("expected a response")
	}
	if withTools != 0 {
		t.Errorf("tools-disabled config must not send tools; got %d tool requests", withTools)
	}
}

func TestThinkFullSendsToolsWhenEnabled(t *testing.T) {
	var withTools, withoutTools int
	o := &OllamaBrain{
		client:  ollamaStub(t, &withTools, &withoutTools),
		model:   "m",
		workDir: t.TempDir(),
		cfg:     &config.Config{MaxHistory: 10},
	}

	// Stub rejects tools and retries without — withTools must still be > 0
	// so we know ToolsEnabled actually attached the schema.
	_, err := o.ThinkFull(context.Background(), "hello", StreamOptions{ToolsEnabled: true})
	if err != nil {
		t.Fatalf("ThinkFull returned error: %v", err)
	}
	if withTools == 0 {
		t.Fatal("ToolsEnabled=true must send a tools request")
	}
}

// ollamaErrorStub serves the chat API with a hard 500 so error-path recovery
// can be exercised without a real server.
func ollamaErrorStub(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestThinkStreamChatErrorReturnsDetailsWithoutSpeech(t *testing.T) {
	// A transport failure reports details without synthesizing an assistant
	// answer or polluting the next turn's history.
	o := &OllamaBrain{client: ollamaErrorStub(t), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "do something hard", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	var got []string
	for c := range stream.Chunks {
		got = append(got, c)
	}
	res := <-stream.Done
	if res.Err == nil || !res.Recovered {
		t.Fatalf("stream done = %+v, want a Recovered error result", res)
	}
	if joined := strings.Join(got, ""); joined != "" {
		t.Fatalf("streamed chunks = %q, want no synthetic speech", joined)
	}
	hist := o.History()
	if len(hist) != 1 || hist[0].Role != "user" {
		t.Fatalf("history tail = %+v, want only the user request recorded", hist)
	}
}

func TestThinkStreamCanceledCtxSkipsRecovery(t *testing.T) {
	// Shutdown and barge-in cancel the turn context; no one is listening for
	// a recovery line, so the raw error must pass through un-recovered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := &OllamaBrain{client: ollamaErrorStub(t), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(ctx, "hi", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	for range stream.Chunks {
	}
	res := <-stream.Done
	if res.Err == nil || res.Recovered {
		t.Fatalf("stream done = %+v, want raw error without recovery on canceled ctx", res)
	}
}

// ollamaPartialThenErrorStub streams one valid content line, then breaks the
// NDJSON stream so the client surfaces an error after partial text arrived.
func ollamaPartialThenErrorStub(t *testing.T) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"Let me check that."},"done":false}`+"\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "not json\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestThinkStreamChatErrorKeepsPartialTextInHistory(t *testing.T) {
	// The user already saw/heard the partial reply, so the next turn's
	// context must contain it — not just the recovery line.
	o := &OllamaBrain{client: ollamaPartialThenErrorStub(t), model: "m", cfg: &config.Config{MaxHistory: 10}}

	stream, err := o.ThinkStream(context.Background(), "check something", StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream() error = %v", err)
	}
	for range stream.Chunks {
	}
	res := <-stream.Done
	if res.Err == nil || !res.Recovered {
		t.Fatalf("stream done = %+v, want a Recovered error result", res)
	}
	hist := o.History()
	tail := hist[len(hist)-1]
	if tail.Content != "Let me check that." {
		t.Fatalf("history tail = %q, want only partial text", tail.Content)
	}
}

func TestOllamaHistoryRoundTripsToolCalls(t *testing.T) {
	call := api.ToolCall{Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": "echo hi"})}}
	o := &OllamaBrain{history: []api.Message{
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "", ToolCalls: []api.ToolCall{call}},
		{Role: "tool", Content: "ok", ToolName: "run_command"},
		{Role: "assistant", Content: "Done."},
	}}

	var restored OllamaBrain
	restored.LoadHistory(o.History())
	if len(restored.history) != 4 {
		t.Fatalf("history len = %d, want 4", len(restored.history))
	}
	got := restored.history[1]
	if got.Role != "assistant" || len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "run_command" {
		t.Fatalf("assistant tool-call = %+v, want run_command preserved", got)
	}
	if restored.history[2].Role != "tool" || restored.history[2].ToolName != "run_command" {
		t.Fatalf("tool result = %+v, want tool_name preserved", restored.history[2])
	}
}

func TestLoadHistoryDropsOrphanedToolPrefix(t *testing.T) {
	// Role sequence from a live Jarvis session whose first user turn was
	// trimmed but whose tool group was kept: Ollama then 500s.
	o := &OllamaBrain{}
	o.LoadHistory([]Turn{
		{Role: "assistant"},
		{Role: "tool", Content: "result 1"},
		{Role: "assistant"},
		{Role: "tool", Content: "result 2"},
		{Role: "assistant", Content: "Done, sir."},
		{Role: "user", Content: "next question"},
		{Role: "assistant", Content: "On it."},
	})
	if len(o.history) == 0 || o.history[0].Role != "assistant" || o.history[0].Content != "Done, sir." {
		t.Fatalf("history = %+v, want orphaned tool prefix dropped through to the speakable assistant", o.history)
	}
	if o.history[1].Role != "user" || o.history[1].Content != "next question" {
		t.Fatalf("history after prefix = %+v, want the following user turn", o.history)
	}
}

func TestLoadHistoryKeepsCompactSummary(t *testing.T) {
	o := &OllamaBrain{}
	o.LoadHistory([]Turn{
		{Role: "assistant", Content: "Earlier we discussed the build."},
		{Role: "user", Content: "and then?"},
	})
	if len(o.history) != 2 || o.history[0].Content != "Earlier we discussed the build." {
		t.Fatalf("history = %+v, want compact summary kept", o.history)
	}
}
