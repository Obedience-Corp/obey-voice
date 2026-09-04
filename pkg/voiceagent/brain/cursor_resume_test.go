package brain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// fakeCursorClient scripts print-mode turns and records the prompts and
// options each attempt used, mirroring fakeGrokClient.
type fakeCursorClient struct {
	results []*cursor.AskResult
	errs    []error

	prompts []string
	opts    []*cursor.AskOptions
}

func (f *fakeCursorClient) AskCtx(_ context.Context, prompt string, opts *cursor.AskOptions) (*cursor.AskResult, error) {
	f.prompts = append(f.prompts, prompt)
	f.opts = append(f.opts, opts)

	i := len(f.prompts) - 1
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.results) {
		return f.results[i], err
	}
	if err != nil {
		return nil, err
	}
	return &cursor.AskResult{Result: "ok"}, nil
}

func newTestCursorBrain(r cursorRunner) *CursorBrain {
	return &CursorBrain{
		client:          r,
		cfg:             &config.Config{AgentName: "Samantha", MaxHistory: 10},
		systemPrompt:    "SYSTEM",
		turnInstruction: "Reply now.",
	}
}

func mustCursorStream(t *testing.T, b *CursorBrain, input string) *Stream {
	t.Helper()
	s, err := b.ThinkStream(t.Context(), input, StreamOptions{})
	if err != nil {
		t.Fatalf("ThinkStream(%q) error = %v", input, err)
	}
	return s
}

func TestCursorStreamCapturesSessionAndResumes(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "One.", SessionID: "csess-1"},
			{Result: "Two.", SessionID: "csess-1"},
		},
	}
	b := newTestCursorBrain(fake)

	collectStream(t, mustCursorStream(t, b, "first"))
	collectStream(t, mustCursorStream(t, b, "second"))

	if got := fake.opts[0].Resume; got != "" {
		t.Fatalf("turn 1 Resume = %q, want empty on the first turn", got)
	}
	if got := fake.opts[1].Resume; got != "csess-1" {
		t.Fatalf("turn 2 Resume = %q, want csess-1", got)
	}
	if p := fake.prompts[1]; strings.Contains(p, "Recent conversation:") || !strings.Contains(p, "User: second") {
		t.Fatalf("turn 2 prompt must be new-text-only:\n%s", p)
	}
	if p := fake.prompts[0]; !strings.Contains(p, "SYSTEM") {
		t.Fatalf("flatten prompt must inline the system prompt:\n%s", p)
	}
	if p := fake.prompts[1]; strings.Contains(p, "SYSTEM") {
		t.Fatalf("resumed prompt must not re-send the system prompt:\n%s", p)
	}
}

func TestCursorStreamFlattensOnlyWithoutSession(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "One."},
			{Result: "Two."},
		},
	}
	b := newTestCursorBrain(fake)

	collectStream(t, mustCursorStream(t, b, "first"))
	collectStream(t, mustCursorStream(t, b, "second"))

	if p := fake.prompts[1]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("turn 2 without a session must flatten history:\n%s", p)
	}
}

func TestCursorStreamRetriesOnceWithoutSessionOnRejectedResume(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "One.", SessionID: "csess-1"},
			nil,
			{Result: "Two.", SessionID: "csess-2"},
		},
		errs: []error{
			nil,
			errors.New("session not found"),
			nil,
		},
	}
	b := newTestCursorBrain(fake)

	collectStream(t, mustCursorStream(t, b, "first"))
	if _, res := collectStream(t, mustCursorStream(t, b, "second")); res.Err != nil {
		t.Fatalf("turn 2 should recover via retry, got %v", res.Err)
	}

	if got := fake.opts[1].Resume; got != "csess-1" {
		t.Fatalf("attempt 2 Resume = %q, want csess-1", got)
	}
	if got := fake.opts[2].Resume; got != "" {
		t.Fatalf("retry Resume = %q, want empty after dropping the session", got)
	}
	if p := fake.prompts[2]; !strings.Contains(p, "Recent conversation:") {
		t.Fatalf("retry must re-flatten history:\n%s", p)
	}
	if b.sessionID != "csess-2" {
		t.Fatalf("sessionID = %q, want the fresh session captured on retry", b.sessionID)
	}
}

func TestCursorThinkFullCapturesSessionAndRetries(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "One.", SessionID: "csess-1"},
			{Result: "won't be used", IsError: true, Subtype: "session_error"},
			{Result: "Two.", SessionID: "csess-2"},
		},
		errs: []error{
			nil,
			errors.New("session_error"),
			nil,
		},
	}
	b := newTestCursorBrain(fake)

	if _, err := b.ThinkFull(t.Context(), "first", StreamOptions{}); err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}
	if b.sessionID != "csess-1" {
		t.Fatalf("sessionID = %q, want csess-1 captured", b.sessionID)
	}

	if _, err := b.ThinkFull(t.Context(), "second", StreamOptions{}); err != nil {
		t.Fatalf("turn 2 should recover via retry, got %v", err)
	}
	if got := fake.opts[1].Resume; got != "csess-1" {
		t.Fatalf("attempt 2 Resume = %q, want csess-1", got)
	}
	if got := fake.opts[2].Resume; got != "" {
		t.Fatalf("retry Resume = %q, want empty", got)
	}
	if b.sessionID != "csess-2" {
		t.Fatalf("sessionID = %q, want csess-2 after retry", b.sessionID)
	}
	if p := fake.prompts[1]; strings.Contains(p, "Recent conversation:") || !strings.Contains(p, "User: second") {
		t.Fatalf("resumed blocking prompt must be new-text-only:\n%s", p)
	}
}

func TestCursorDropsSessionOnClearAndLoadHistory(t *testing.T) {
	b := newTestCursorBrain(&fakeCursorClient{})
	b.sessionID = "csess-1"
	b.promptTokens = 99

	b.ClearHistory()
	if b.sessionID != "" {
		t.Fatalf("ClearHistory: sessionID = %q, want empty", b.sessionID)
	}
	if b.promptTokens != 0 {
		t.Fatalf("ClearHistory: promptTokens = %d, want 0", b.promptTokens)
	}

	b.sessionID = "csess-2"
	b.promptTokens = 42
	b.LoadHistory([]Turn{{Role: "user", Content: "hi"}})
	if b.sessionID != "" {
		t.Fatalf("LoadHistory: sessionID = %q, want empty", b.sessionID)
	}
	if b.promptTokens != 0 {
		t.Fatalf("LoadHistory: promptTokens = %d, want 0", b.promptTokens)
	}
	if len(b.history) != 1 {
		t.Fatalf("LoadHistory history len = %d, want 1", len(b.history))
	}
}

func TestCursorFailedFirstTurnDoesNotCaptureSession(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "error text", SessionID: "csess-bad", IsError: true, Usage: cursor.Usage{InputTokens: 12}},
			{Result: "One.", SessionID: "csess-1"},
		},
		errs: []error{errors.New("boom"), nil},
	}
	b := newTestCursorBrain(fake)

	if _, err := b.ThinkFull(t.Context(), "first", StreamOptions{}); err == nil {
		t.Fatal("ThinkFull() error = nil, want the first-turn failure")
	}
	if b.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty so a failed first turn cannot start a resume", b.sessionID)
	}
	if b.promptTokens != 0 {
		t.Fatalf("promptTokens = %d, want 0 after a failed attempt", b.promptTokens)
	}

	if _, err := b.ThinkFull(t.Context(), "second", StreamOptions{}); err != nil {
		t.Fatalf("turn 2 error = %v", err)
	}
	if got := fake.opts[1].Resume; got != "" {
		t.Fatalf("turn 2 Resume = %q, want empty (flatten, not resume)", got)
	}
	if p := fake.prompts[1]; !strings.Contains(p, "SYSTEM") {
		t.Fatalf("turn 2 after a failed first turn must flatten with the system prompt:\n%s", p)
	}
}

func TestCursorAskAttemptFailureDoesNotStampSession(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{
			{Result: "nope", SessionID: "csess-bad", IsError: true, Usage: cursor.Usage{InputTokens: 12}},
		},
		errs: []error{errors.New("boom")},
	}
	b := newTestCursorBrain(fake)
	b.sessionID = "csess-1"
	b.promptTokens = 7
	b.history = []Turn{{Role: "user", Content: "hi"}}

	if _, err := b.askAttempt(t.Context(), StreamOptions{}); err == nil {
		t.Fatal("askAttempt() error = nil, want the CLI failure")
	}
	if b.sessionID != "csess-1" {
		t.Fatalf("sessionID = %q, want csess-1 left in place", b.sessionID)
	}
	if b.promptTokens != 7 {
		t.Fatalf("promptTokens = %d, want 7 left in place", b.promptTokens)
	}
}

func TestCursorSessionInfoReportsLiveSession(t *testing.T) {
	b := newTestCursorBrain(&fakeCursorClient{})
	b.sessionID = "csess-12345678"
	b.promptTokens = 42
	b.history = []Turn{{Role: "user", Content: "hi"}}

	want := SessionState{Kind: "harness", ID: "csess-12345678", PromptTokens: 42, Turns: 1}
	if got := b.SessionInfo(); got != want {
		t.Fatalf("SessionInfo() = %+v, want %+v", got, want)
	}
}

func TestCursorStreamSpeaksCleanedText(t *testing.T) {
	fake := &fakeCursorClient{
		results: []*cursor.AskResult{{Result: "<think>secret</think>Hello there."}},
	}
	b := newTestCursorBrain(fake)

	text, res := collectStream(t, mustCursorStream(t, b, "hello"))
	if res.Err != nil {
		t.Fatalf("stream result error = %v", res.Err)
	}
	if strings.Contains(text, "secret") || strings.Contains(text, "<think>") {
		t.Fatalf("streamed uncleaned text %q", text)
	}
	if !strings.Contains(text, "Hello there.") {
		t.Fatalf("streamed %q, want the cleaned reply", text)
	}
	if got := b.history[len(b.history)-1].Content; got != text {
		t.Fatalf("history %q != spoken %q", got, text)
	}
}

func TestCursorRefreshDropsSessionWhenPersonaChanges(t *testing.T) {
	b := newTestCursorBrain(&fakeCursorClient{})
	b.sessionID = "csess-1"
	b.promptTokens = 99
	b.systemPrompt = "OLD"
	b.personaReloader = newPromptReloader(prompts.KindPersona, "", "OLD PERSONA", nil)

	b.refreshPrompts(nil)
	if b.sessionID != "" {
		t.Fatalf("sessionID = %q, want empty after the system prompt changed", b.sessionID)
	}
	if b.promptTokens != 0 {
		t.Fatalf("promptTokens = %d, want 0 after dropSession", b.promptTokens)
	}
	if b.systemPrompt == "OLD" {
		t.Fatal("systemPrompt was not rebuilt")
	}
}

func TestCursorRefreshKeepsSessionWhenPersonaUnchanged(t *testing.T) {
	cfg := &config.Config{AgentName: "Samantha", EnvironmentContextEnabled: true, MaxHistory: 10}
	persona, err := personaSystemPrompt(cfg)
	if err != nil {
		t.Fatalf("personaSystemPrompt: %v", err)
	}
	b := newTestCursorBrain(&fakeCursorClient{})
	b.cfg = cfg
	b.sessionID = "csess-1"
	b.promptTokens = 7
	b.systemPrompt = b.assembleSystem(persona)
	b.personaReloader = newPromptReloader(prompts.KindPersona, cfg.Persona, persona, nil)

	b.refreshPrompts(nil)
	if b.sessionID != "csess-1" {
		t.Fatalf("sessionID = %q, want csess-1 when the system prompt is unchanged", b.sessionID)
	}
	if b.promptTokens != 7 {
		t.Fatalf("promptTokens = %d, want 7", b.promptTokens)
	}
}

func TestCursorAskOptionsGateToolsOnVoiceToolsEnabled(t *testing.T) {
	b := &CursorBrain{cfg: &config.Config{CursorModel: "composer-2.5"}}

	disabled := b.askOptions(false)
	if disabled.Mode != cursor.ModeAsk {
		t.Fatalf("disabled Mode = %q, want %q", disabled.Mode, cursor.ModeAsk)
	}
	if !disabled.Trust {
		t.Fatal("disabled Trust = false, want true so a first-run workspace cannot stall")
	}
	if disabled.Force || disabled.Yolo || disabled.AllowDangerousMode {
		t.Fatalf("disabled must not enable force/yolo: %+v", disabled)
	}

	enabled := b.askOptions(true)
	if enabled.Mode != cursor.ModeUnset {
		t.Fatalf("enabled Mode = %q, want unset; print mode rejects --mode agent", enabled.Mode)
	}
	if enabled.Model != "composer-2.5" {
		t.Fatalf("Model = %q, want composer-2.5", enabled.Model)
	}
	if enabled.Force || enabled.Yolo || enabled.AllowDangerousMode {
		t.Fatalf("tools-on must not set force/yolo: voice_tools_enabled is not a dangerous-mode opt-in: %+v", enabled)
	}
	if err := enabled.Validate(); err != nil {
		t.Fatalf("enabled options rejected by SDK: %v", err)
	}
	args := cursor.BuildPrintArgs("prompt", enabled)
	if slices.Index(args, "--force") >= 0 {
		t.Fatalf("tools-on args = %#v, did not want --force", args)
	}
	if slices.Index(args, "--mode") >= 0 {
		t.Fatalf("tools-on args = %#v, did not want --mode (CLI print mode allows only plan/ask)", args)
	}
}

func TestWrapCursorAskErrorIncludesStderr(t *testing.T) {
	err := wrapCursorAskError(&cursor.Error{
		Kind:    cursor.KindValidation,
		Message: "cursor-agent ask produced no output on stdout",
		Stderr:  "error: option '--mode <mode>' argument 'agent' is invalid. Allowed choices are plan, ask.\n",
	})
	got := err.Error()
	if !strings.Contains(got, "no output on stdout") {
		t.Fatalf("error = %q, want the SDK message", got)
	}
	if !strings.Contains(got, "argument 'agent' is invalid") {
		t.Fatalf("error = %q, want stderr so the activity feed names the real CLI failure", got)
	}
}
