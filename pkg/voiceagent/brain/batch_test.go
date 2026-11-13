package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// fakeBatchProvider is the in-package fake for BatchProvider consumers.
type fakeBatchProvider struct {
	result BatchResult
	err    error
	got    []BatchRequest
}

var _ BatchProvider = (*fakeBatchProvider)(nil)

func (f *fakeBatchProvider) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	f.got = append(f.got, req)
	if f.err != nil {
		return BatchResult{}, f.err
	}
	return f.result, nil
}

// batchStub serves the Ollama chat API with a fixed response content,
// recording each request body.
func batchStub(t *testing.T, content string, status int, bodies *[][]byte) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, body)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"boom"}`)
			return
		}
		resp, _ := json.Marshal(map[string]any{
			"model":       "m",
			"message":     map[string]string{"role": "assistant", "content": content},
			"done":        true,
			"done_reason": "stop",
		})
		_, _ = w.Write(append(resp, '\n'))
	}))
	t.Cleanup(srv.Close)

	base, _ := url.Parse(srv.URL)
	return api.NewClient(base, http.DefaultClient)
}

func TestOllamaBatchTransformErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		status  int
		wantErr string
	}{
		{"provider failure", "", http.StatusInternalServerError, "ollama batch:"},
		{"empty response", "", http.StatusOK, "empty response"},
		{"whitespace-only response", "  \n ", http.StatusOK, "empty response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodies [][]byte
			o := &ollamaBatch{client: batchStub(t, tt.content, tt.status, &bodies), model: "m"}

			_, err := o.Transform(context.Background(), BatchRequest{Text: "hello"})
			if err == nil {
				t.Fatal("Transform() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Transform() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestOllamaBatchTransformCancelledBeforeCall(t *testing.T) {
	var bodies [][]byte
	o := &ollamaBatch{client: batchStub(t, "hi", http.StatusOK, &bodies), model: "m"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := o.Transform(ctx, BatchRequest{Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transform() error = %v, want context.Canceled", err)
	}
	if len(bodies) != 0 {
		t.Fatalf("cancelled context must not reach the provider; got %d requests", len(bodies))
	}
}

func TestOllamaBatchTransformHonorsCancellationDuringCall(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// Block until the client cancels (or the test releases us), so the
		// call is genuinely in-flight when the context is cancelled.
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Release before Close (defers run LIFO) so a handler that never sees the
	// cancellation still returns and Close cannot hang.
	defer srv.Close()
	defer close(release)

	base, _ := url.Parse(srv.URL)
	o := &ollamaBatch{client: api.NewClient(base, http.DefaultClient), model: "m"}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := o.Transform(ctx, BatchRequest{Text: "hello"})
		errc <- err
	}()

	<-started
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Transform() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transform() did not return promptly after cancellation")
	}
}

func TestOllamaBatchTransformSendsSystemAndBody(t *testing.T) {
	var bodies [][]byte
	o := &ollamaBatch{client: batchStub(t, " transformed ", http.StatusOK, &bodies), model: "m"}

	req := BatchRequest{
		SystemPrompt: "You rewrite text for narration.",
		StylePrompt:  "Warm and steady.",
		SectionTitle: "Chapter 1",
		Text:         "Raw text.",
	}
	res, err := o.Transform(context.Background(), req)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}

	if res.Text != "transformed" {
		t.Errorf("Text = %q, want trimmed %q", res.Text, "transformed")
	}
	if res.Provider != "ollama" || res.Model != "m" {
		t.Errorf("identity = %s/%s, want ollama/m", res.Provider, res.Model)
	}

	if len(bodies) != 1 {
		t.Fatalf("got %d requests, want 1", len(bodies))
	}
	var sent api.ChatRequest
	if err := json.Unmarshal(bodies[0], &sent); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(sent.Messages) != 2 {
		t.Fatalf("got %d messages, want system + user", len(sent.Messages))
	}
	if sent.Messages[0].Role != "system" || sent.Messages[0].Content != req.SystemPrompt {
		t.Errorf("system message = %+v, want caller's system prompt verbatim", sent.Messages[0])
	}
	if sent.Messages[1].Role != "user" || sent.Messages[1].Content != batchPromptBody(req) {
		t.Errorf("user message = %+v, want assembled body %q", sent.Messages[1], batchPromptBody(req))
	}
	if sent.Tools != nil {
		t.Error("batch transform must not send tools")
	}
	if sent.Stream == nil || *sent.Stream {
		t.Error("batch transform must not stream")
	}
}

func TestOllamaBatchTransformOmitsEmptySystemMessage(t *testing.T) {
	var bodies [][]byte
	o := &ollamaBatch{client: batchStub(t, "out", http.StatusOK, &bodies), model: "m"}

	if _, err := o.Transform(context.Background(), BatchRequest{Text: "hello"}); err != nil {
		t.Fatalf("Transform() error = %v", err)
	}

	var sent api.ChatRequest
	if err := json.Unmarshal(bodies[0], &sent); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want single user message", sent.Messages)
	}
}

type fakeGrokRunner struct {
	result    *grok.GrokResult
	err       error
	gotPrompt string
	gotOpts   *grok.RunOptions
	calls     int
}

func (f *fakeGrokRunner) RunPromptCtx(_ context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error) {
	f.calls++
	f.gotPrompt = prompt
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestGrokBatchTransformErrors(t *testing.T) {
	tests := []struct {
		name    string
		runner  *fakeGrokRunner
		wantErr string
	}{
		{"provider failure", &fakeGrokRunner{err: errors.New("boom")}, "grok batch:"},
		{"empty response", &fakeGrokRunner{result: &grok.GrokResult{Text: ""}}, "empty response"},
		{"whitespace-only response", &fakeGrokRunner{result: &grok.GrokResult{Text: " \n "}}, "empty response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &grokBatch{runner: tt.runner, model: "m"}

			_, err := g.Transform(context.Background(), BatchRequest{Text: "hello"})
			if err == nil {
				t.Fatal("Transform() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Transform() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestGrokBatchTransformCancelledBeforeCall(t *testing.T) {
	runner := &fakeGrokRunner{result: &grok.GrokResult{Text: "out"}}
	g := &grokBatch{runner: runner, model: "m"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.Transform(ctx, BatchRequest{Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transform() error = %v, want context.Canceled", err)
	}
	if runner.calls != 0 {
		t.Fatalf("cancelled context must not reach the provider; got %d calls", runner.calls)
	}
}

func TestGrokBatchTransformSendsSystemAndBody(t *testing.T) {
	runner := &fakeGrokRunner{result: &grok.GrokResult{Text: " transformed "}}
	g := &grokBatch{runner: runner, model: "grok-4"}

	req := BatchRequest{
		SystemPrompt: "You rewrite text for narration.",
		StylePrompt:  "Warm and steady.",
		Text:         "Raw text.",
	}
	res, err := g.Transform(context.Background(), req)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}

	if res.Text != "transformed" {
		t.Errorf("Text = %q, want trimmed %q", res.Text, "transformed")
	}
	if res.Provider != "grok" || res.Model != "grok-4" {
		t.Errorf("identity = %s/%s, want grok/grok-4", res.Provider, res.Model)
	}
	if runner.gotPrompt != batchPromptBody(req) {
		t.Errorf("prompt = %q, want assembled body %q", runner.gotPrompt, batchPromptBody(req))
	}
	if runner.gotOpts.SystemPromptOverride != req.SystemPrompt {
		t.Errorf("SystemPromptOverride = %q, want caller's system prompt verbatim", runner.gotOpts.SystemPromptOverride)
	}
	if runner.gotOpts.Model != "grok-4" {
		t.Errorf("Model = %q, want grok-4", runner.gotOpts.Model)
	}
}

func TestGrokBatchReportsDefaultModelWhenUnconfigured(t *testing.T) {
	runner := &fakeGrokRunner{result: &grok.GrokResult{Text: "out"}}
	g := &grokBatch{runner: runner}

	res, err := g.Transform(context.Background(), BatchRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	if res.Provider != "grok" || res.Model != "default" {
		t.Errorf("identity = %s/%s, want grok/default", res.Provider, res.Model)
	}
}

type fakeCursorRunner struct {
	result    *cursor.AskResult
	err       error
	gotPrompt string
	gotOpts   *cursor.AskOptions
	calls     int
}

func (f *fakeCursorRunner) AskCtx(_ context.Context, prompt string, opts *cursor.AskOptions) (*cursor.AskResult, error) {
	f.calls++
	f.gotPrompt = prompt
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestCursorBatchTransformErrors(t *testing.T) {
	tests := []struct {
		name    string
		runner  *fakeCursorRunner
		wantErr string
	}{
		{"provider failure", &fakeCursorRunner{err: errors.New("boom")}, "cursor batch:"},
		{"empty response", &fakeCursorRunner{result: &cursor.AskResult{Result: ""}}, "empty response"},
		{"whitespace-only response", &fakeCursorRunner{result: &cursor.AskResult{Result: " \n "}}, "empty response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &cursorBatch{runner: tt.runner, model: "m"}

			_, err := c.Transform(t.Context(), BatchRequest{Text: "hello"})
			if err == nil {
				t.Fatal("Transform() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Transform() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCursorBatchTransformCancelledBeforeCall(t *testing.T) {
	runner := &fakeCursorRunner{result: &cursor.AskResult{Result: "out"}}
	c := &cursorBatch{runner: runner, model: "m"}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := c.Transform(ctx, BatchRequest{Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transform() error = %v, want context.Canceled", err)
	}
	if runner.calls != 0 {
		t.Fatalf("cancelled context must not reach the provider; got %d calls", runner.calls)
	}
}

func TestCursorBatchTransformSendsAssembledPrompt(t *testing.T) {
	runner := &fakeCursorRunner{result: &cursor.AskResult{Result: " transformed "}}
	c := &cursorBatch{runner: runner, model: "composer-2.5"}

	req := BatchRequest{
		SystemPrompt: "You rewrite text for narration.",
		StylePrompt:  "Warm and steady.",
		Text:         "Raw text.",
	}
	res, err := c.Transform(t.Context(), req)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}

	if res.Text != "transformed" {
		t.Errorf("Text = %q, want trimmed %q", res.Text, "transformed")
	}
	if res.Provider != "cursor" || res.Model != "composer-2.5" {
		t.Errorf("identity = %s/%s, want cursor/composer-2.5", res.Provider, res.Model)
	}
	if runner.gotPrompt != AssembleBatchPrompt(req) {
		t.Errorf("prompt = %q, want assembled prompt %q", runner.gotPrompt, AssembleBatchPrompt(req))
	}
	if runner.gotOpts.Mode != cursor.ModeAsk {
		t.Errorf("Mode = %q, want ask so batch cannot edit", runner.gotOpts.Mode)
	}
	if !runner.gotOpts.Trust {
		t.Error("Trust = false, want true")
	}
	if runner.gotOpts.Model != "composer-2.5" {
		t.Errorf("Model = %q, want composer-2.5", runner.gotOpts.Model)
	}
}

func TestCursorBatchReportsDefaultModelWhenUnconfigured(t *testing.T) {
	runner := &fakeCursorRunner{result: &cursor.AskResult{Result: "out"}}
	c := &cursorBatch{runner: runner}

	res, err := c.Transform(t.Context(), BatchRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	if res.Provider != "cursor" || res.Model != "default" {
		t.Errorf("identity = %s/%s, want cursor/default", res.Provider, res.Model)
	}
}

func TestClaudeBatchAsksForTheConfiguredModel(t *testing.T) {
	dir := t.TempDir()
	// The constructor only looks the CLI up; the fake runner answers.
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	c, err := newClaudeBatch(&config.Config{BrainProvider: "claude", ClaudeModel: "haiku", PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeClaudeRunner{result: &claude.ClaudeResult{Result: "out"}}
	c.runner = runner
	res, err := c.Transform(t.Context(), BatchRequest{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if runner.gotOpts.Model != "haiku" || res.Model != "haiku" {
		t.Fatalf("Model = %q, reported %q; want haiku", runner.gotOpts.Model, res.Model)
	}
}

func TestClaudeBatchFailureNamesWhatTheCLIPrinted(t *testing.T) {
	cliErr := claude.ParseError("Loading config\nError: invalid model: soonet\n", 1)
	c := &claudeBatch{runner: &fakeClaudeRunner{err: cliErr}, model: "soonet"}
	_, err := c.Transform(t.Context(), BatchRequest{Text: "hello"})
	var got *claude.ClaudeError
	if err == nil || !strings.HasSuffix(err.Error(), ": Error: invalid model: soonet") || !errors.As(err, &got) {
		t.Fatalf("err = %v", err)
	}
}

func TestClaudeFailureCutsALongLineOnARune(t *testing.T) {
	err := claudeFailure("claude batch", claude.ParseError(strings.Repeat("é", 300), 1))
	if !utf8.ValidString(err.Error()) || !strings.HasSuffix(err.Error(), strings.Repeat("é", 240)+"…") {
		t.Fatalf("err = %q", err)
	}
}

type fakeClaudeRunner struct {
	result    *claude.ClaudeResult
	err       error
	gotPrompt string
	gotOpts   *claude.RunOptions
	calls     int
}

func (f *fakeClaudeRunner) RunPromptCtx(_ context.Context, prompt string, opts *claude.RunOptions) (*claude.ClaudeResult, error) {
	f.calls++
	f.gotPrompt = prompt
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestClaudeBatchTransformErrors(t *testing.T) {
	tests := []struct {
		name    string
		runner  *fakeClaudeRunner
		wantErr string
	}{
		{"provider failure", &fakeClaudeRunner{err: errors.New("boom")}, "claude batch:"},
		{"empty response", &fakeClaudeRunner{result: &claude.ClaudeResult{Result: ""}}, "empty response"},
		{"whitespace-only response", &fakeClaudeRunner{result: &claude.ClaudeResult{Result: " \n "}}, "empty response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &claudeBatch{runner: tt.runner}

			_, err := c.Transform(context.Background(), BatchRequest{Text: "hello"})
			if err == nil {
				t.Fatal("Transform() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Transform() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestClaudeBatchTransformCancelledBeforeCall(t *testing.T) {
	runner := &fakeClaudeRunner{result: &claude.ClaudeResult{Result: "out"}}
	c := &claudeBatch{runner: runner}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Transform(ctx, BatchRequest{Text: "hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transform() error = %v, want context.Canceled", err)
	}
	if runner.calls != 0 {
		t.Fatalf("cancelled context must not reach the provider; got %d calls", runner.calls)
	}
}

func TestClaudeBatchTransformSendsSystemAndBodyWithoutPersonaOrTools(t *testing.T) {
	runner := &fakeClaudeRunner{result: &claude.ClaudeResult{Result: " transformed "}}
	c := &claudeBatch{runner: runner}

	req := BatchRequest{
		SystemPrompt: "You rewrite text for narration.",
		StylePrompt:  "Warm and steady.",
		Text:         "Raw text.",
	}
	res, err := c.Transform(context.Background(), req)
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}

	if res.Text != "transformed" {
		t.Errorf("Text = %q, want trimmed %q", res.Text, "transformed")
	}
	if res.Provider != "claude" || res.Model != "default" {
		t.Errorf("identity = %s/%s, want claude/default", res.Provider, res.Model)
	}
	if runner.gotPrompt != batchPromptBody(req) {
		t.Errorf("prompt = %q, want assembled body %q", runner.gotPrompt, batchPromptBody(req))
	}

	opts := runner.gotOpts
	if opts.SystemPrompt != req.SystemPrompt {
		t.Errorf("SystemPrompt = %q, want caller's system prompt verbatim", opts.SystemPrompt)
	}
	// No interactive persona may leak into a batch transform.
	if strings.Contains(opts.SystemPrompt, "inspired by the character") {
		t.Error("batch transform must not inject the interactive persona system prompt")
	}
	if opts.Format != claude.TextOutput {
		t.Errorf("Format = %v, want TextOutput", opts.Format)
	}
	// Batch is a pure text transform: it must not grant tool execution.
	if opts.PermissionMode == claude.PermissionModeBypassPermissions {
		t.Error("batch transform must not use bypassPermissions (would grant tool execution)")
	}
	if len(opts.AllowedTools) != 0 || len(opts.Tools) != 0 {
		t.Errorf("batch transform must wire no tools; got AllowedTools=%v Tools=%v", opts.AllowedTools, opts.Tools)
	}
}

// TestAssembleBatchPromptOrder locks the canonical part order — downstream
// caching hashes this string, so any change here invalidates caches.
func TestAssembleBatchPromptOrder(t *testing.T) {
	tests := []struct {
		name string
		req  BatchRequest
		want string
	}{
		{
			name: "all parts in documented order, metadata sorted",
			req: BatchRequest{
				SystemPrompt:  "SYSTEM",
				StylePrompt:   "STYLE",
				Pronunciation: "PRONUNCIATION",
				SectionTitle:  "Chapter 1",
				Text:          "TEXT",
				Metadata:      map[string]string{"c": "3", "a": "1", "b": "2"},
			},
			want: "SYSTEM\n\nSTYLE\n\nPRONUNCIATION\n\nSection: Chapter 1\n\na: 1\nb: 2\nc: 3\n\nTEXT",
		},
		{
			name: "empty parts skipped without extra blank lines",
			req: BatchRequest{
				SystemPrompt: "SYSTEM",
				Text:         "TEXT",
			},
			want: "SYSTEM\n\nTEXT",
		},
		{
			name: "text only",
			req:  BatchRequest{Text: "TEXT"},
			want: "TEXT",
		},
		{
			name: "empty request",
			req:  BatchRequest{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 5; i++ {
				if got := AssembleBatchPrompt(tt.req); got != tt.want {
					t.Fatalf("AssembleBatchPrompt() = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestFakeBatchProviderRecordsRequests(t *testing.T) {
	f := &fakeBatchProvider{result: BatchResult{Text: "out", Provider: "fake", Model: "fake-1"}}

	res, err := f.Transform(context.Background(), BatchRequest{Text: "in"})
	if err != nil {
		t.Fatalf("Transform() error = %v", err)
	}
	if res.Text != "out" {
		t.Errorf("Text = %q, want out", res.Text)
	}
	if len(f.got) != 1 || f.got[0].Text != "in" {
		t.Errorf("recorded requests = %+v, want the one sent", f.got)
	}
}

// TestNewOllamaBatchWiresRequestPolicy drives the CONSTRUCTOR, because the
// constructor wiring is the fix. Building an ollamaBatch field by field — as
// TestOllamaBatchCarriesRequestPolicy does — proves Transform forwards whatever
// it is handed, and would keep passing if newOllamaBatch went back to handing it
// nothing. In particular a nil `think` is exactly what the bug looked like, and
// a hand-built struct with think:nil passes the other test's shape.
func TestNewOllamaBatchWiresRequestPolicy(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_, _ = io.WriteString(w, `{"models":[{"name":"test-model"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		_, _ = io.WriteString(w, `{"model":"test-model","message":{"role":"assistant","content":"out"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)

	o, err := newOllamaBatch(&config.Config{
		OllamaModel:            "test-model",
		OllamaHost:             srv.URL,
		OllamaNumCtx:           8192,
		OllamaNumBatch:         128,
		OllamaNumBatchExplicit: true,
		OllamaKeepAlive:        "10m",
		OllamaThink:            "false",
		WorkDir:                t.TempDir(),
	})
	if err != nil {
		t.Fatalf("newOllamaBatch: %v", err)
	}
	if _, err := o.Transform(context.Background(), BatchRequest{Text: "hello"}); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("got %d chat requests, want 1", len(bodies))
	}

	var got struct {
		Options   map[string]any `json:"options"`
		KeepAlive any            `json:"keep_alive"`
		Think     *bool          `json:"think"`
	}
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatalf("request body: %v", err)
	}
	for k, want := range map[string]int{"num_ctx": 8192, "num_batch": 128} {
		v, ok := got.Options[k]
		if !ok {
			t.Errorf("%s absent: the constructor did not carry the interactive request options", k)
			continue
		}
		if int(v.(float64)) != want {
			t.Errorf("%s = %v, want %d", k, v, want)
		}
	}
	if got.KeepAlive == nil {
		t.Error("keep_alive absent: batch would fall back to the server default and evict the warm conversational model")
	}
	// The one that made the original bug invisible: batch output is read, not
	// spoken, so a reasoning model reasoning here costs time and nothing warns.
	if got.Think == nil {
		t.Error("think absent: a reasoning model would reason into a file")
	} else if *got.Think {
		t.Errorf("think = true, want false from ollama_think")
	}
}

// TestOllamaBatchCarriesRequestPolicy pins B2's finding at the third call site.
// Batch runs against the same Ollama, and usually the same model, as the
// conversation. Ollama reloads the runner whenever a request's options differ
// from the loaded ones, so a bare batch call evicts the warm conversational
// runner and the next spoken turn evicts it back — a model load on each side of
// every transform. The missing think value is the same bug this work was about:
// omitted, a reasoning model reasons, and batch output is read rather than
// heard, so nobody notices except as a slow, padded transform.
func TestOllamaBatchCarriesRequestPolicy(t *testing.T) {
	cfg := &config.Config{
		OllamaNumCtx:    8192,
		OllamaNumBatch:  128,
		OllamaKeepAlive: "10m",
		OllamaThink:     "false",
	}
	interactive := &OllamaBrain{model: "m", cfg: cfg, keepAlive: &api.Duration{Duration: 10 * time.Minute}}

	var bodies [][]byte
	o := &ollamaBatch{
		client:    batchStub(t, "out", http.StatusOK, &bodies),
		model:     "m",
		options:   interactive.requestOptions(),
		keepAlive: interactive.keepAlive,
		think:     resolveOllamaThink(cfg.OllamaThink, "m"),
	}
	if _, err := o.Transform(context.Background(), BatchRequest{Text: "hello"}); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("got %d requests, want 1", len(bodies))
	}

	var got struct {
		Options   map[string]any `json:"options"`
		KeepAlive any            `json:"keep_alive"`
		Think     *bool          `json:"think"`
	}
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatalf("request body: %v", err)
	}
	for _, k := range []string{"num_ctx", "num_batch"} {
		want := interactive.newChatRequest(nil, nil).Options[k]
		if int(got.Options[k].(float64)) != want.(int) {
			t.Errorf("batch %s = %v, interactive %s = %v: a mismatch reloads the runner", k, got.Options[k], k, want)
		}
	}
	if got.KeepAlive == nil {
		t.Error("keep_alive absent: the batch call would fall back to the server default and evict the warm model early")
	}
	if got.Think == nil || *got.Think {
		t.Errorf("think = %v, want an explicit false so a reasoning model does not reason into a file", got.Think)
	}
}

func TestOllamaBatchResolveThink(t *testing.T) {
	if got := resolveOllamaThink("false", "qwen3:4b"); got == nil || got.Value != false {
		t.Errorf("explicit false on qwen3 = %#v, want false", got)
	}
	if got := resolveOllamaThink("auto", "qwen3:4b"); got == nil || got.Value != false {
		t.Errorf("qwen3+auto = %#v, want false", got)
	}
	if got := resolveOllamaThink("auto", "m"); got == nil || got.Value != false {
		t.Errorf("non-qwen3+auto = %#v, want false", got)
	}
}
