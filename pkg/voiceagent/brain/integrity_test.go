package brain

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestEvaluateStreamIntegrityDoneReason(t *testing.T) {
	tests := []struct {
		name       string
		doneReason string
		wantTrunc  bool
	}{
		{"length", "length", true},
		{"stop", "stop", false},
		{"missing", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateStreamIntegrity(IntegrityInput{
				DoneReason: tt.doneReason,
				NumCtx:     8192,
				Speakable:  "Complete sentence.",
				GenTokens:  100,
			})
			if got.Truncated != tt.wantTrunc {
				t.Fatalf("Truncated = %v, want %v (done_reason=%q)", got.Truncated, tt.wantTrunc, tt.doneReason)
			}
			if got.DoneReason != strings.TrimSpace(tt.doneReason) {
				t.Fatalf("DoneReason = %q, want %q", got.DoneReason, tt.doneReason)
			}
		})
	}
}

func TestEvaluateStreamIntegrityHeuristicTable(t *testing.T) {
	tests := []struct {
		name        string
		in          IntegrityInput
		wantTrunc   bool
		wantMidSent bool
	}{
		{
			name: "mid_sentence_low_gen_no_tools",
			in: IntegrityInput{
				DoneReason: "stop",
				NumCtx:     8192,
				Speakable:  "The incident occurred when the context",
				GenTokens:  49,
			},
			wantTrunc:   true,
			wantMidSent: true,
		},
		{
			name: "terminal_punctuation_ok",
			in: IntegrityInput{
				DoneReason: "stop",
				NumCtx:     8192,
				Speakable:  "All done.",
				GenTokens:  20,
			},
			wantTrunc:   false,
			wantMidSent: false,
		},
		{
			name: "ellipsis_terminal",
			in: IntegrityInput{
				Speakable: "Wait…",
				GenTokens: 10,
				NumCtx:    8192,
			},
			wantTrunc:   false,
			wantMidSent: false,
		},
		{
			name: "high_gen_not_mid_sentence",
			in: IntegrityInput{
				Speakable: "Cut off without punctuation but long generation",
				GenTokens: 200,
				NumCtx:    8192,
			},
			wantTrunc:   false,
			wantMidSent: false,
		},
		{
			name: "tool_calls_skip_heuristic",
			in: IntegrityInput{
				Speakable:    "partial",
				GenTokens:    10,
				HasToolCalls: true,
				NumCtx:       8192,
			},
			wantTrunc:   false,
			wantMidSent: false,
		},
		{
			name: "prompt_saturation",
			in: IntegrityInput{
				DoneReason:   "stop",
				PromptTokens: 8160,
				NumCtx:       8192,
				Speakable:    "Done.",
				GenTokens:    10,
			},
			wantTrunc:   true,
			wantMidSent: false,
		},
		{
			name: "empty_speakable_not_mid_sentence",
			in: IntegrityInput{
				GenTokens: 10,
				NumCtx:    8192,
			},
			wantTrunc:   false,
			wantMidSent: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateStreamIntegrity(tt.in)
			if got.Truncated != tt.wantTrunc {
				t.Fatalf("Truncated = %v, want %v", got.Truncated, tt.wantTrunc)
			}
			if got.MidSentence != tt.wantMidSent {
				t.Fatalf("MidSentence = %v, want %v", got.MidSentence, tt.wantMidSent)
			}
		})
	}
}

func TestEndsMidSentence(t *testing.T) {
	for text, want := range map[string]bool{
		"hello":                    true,
		"All done. [sigh]":         false,
		"All done. [laugh] [sigh]": false,
		"The next step is [sigh]":  true,
		"[sigh]":                   false,
		"hello.":                   false,
		"really?":                  false,
		"wow!":                     false,
		"wait…":                    false,
		"  trim  ":                 true,
		"":                         false,
	} {
		if got := endsMidSentence(text); got != want {
			t.Errorf("endsMidSentence(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestThinkStreamIntegrityFromStub(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		wantTrunc bool
	}{
		{
			name: "done_reason_length",
			lines: []string{
				`{"model":"m","message":{"role":"assistant","content":"The answer stops mid"},"done":true,"done_reason":"length","prompt_eval_count":100,"eval_count":12}`,
			},
			wantTrunc: true,
		},
		{
			name: "done_reason_stop_complete",
			lines: []string{
				`{"model":"m","message":{"role":"assistant","content":"All good."},"done":true,"done_reason":"stop","prompt_eval_count":100,"eval_count":12}`,
			},
			wantTrunc: false,
		},
		{
			name: "missing_done_reason_mid_sentence",
			lines: []string{
				`{"model":"m","message":{"role":"assistant","content":"Still talking"},"done":true,"prompt_eval_count":100,"eval_count":20}`,
			},
			wantTrunc: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &OllamaBrain{
				client: ollamaStreamStub(t, tt.lines),
				model:  "m",
				cfg:    &config.Config{MaxHistory: 10, ContinuityIntegrityEnabled: true},
			}
			stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, result := collectStream(t, stream)
			if result.Integrity.Truncated != tt.wantTrunc {
				t.Fatalf("Integrity.Truncated = %v, want %v", result.Integrity.Truncated, tt.wantTrunc)
			}
		})
	}
}

func TestThinkStreamIntegrityDisabled(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Cut off"},"done":true,"done_reason":"length","prompt_eval_count":100,"eval_count":12}`,
	}
	o := &OllamaBrain{
		client: ollamaStreamStub(t, lines),
		model:  "m",
		cfg:    &config.Config{MaxHistory: 10, ContinuityIntegrityEnabled: false},
	}
	var got []events.Event
	o.SetContinuitySink(func(e events.Event) { got = append(got, e) })
	stream, err := o.ThinkStream(context.Background(), "hi", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, result := collectStream(t, stream)
	if result.Integrity.Truncated {
		t.Fatal("expected zero integrity when disabled")
	}
	if len(got) != 0 {
		t.Fatalf("continuity events = %d, want 0", len(got))
	}
}

func TestContinuityBudgetEmittedOnTurn(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Hi."},"done":true,"done_reason":"stop"}`,
	}
	o := &OllamaBrain{
		client:           ollamaStreamStub(t, lines),
		model:            "m",
		fullSystemPrompt: "system",
		cfg:              &config.Config{MaxHistory: 10, OllamaNumCtx: 8192, ContinuityIntegrityEnabled: true},
	}
	var mu sync.Mutex
	var budgets []events.ContinuityBudget
	o.SetContinuitySink(func(e events.Event) {
		if b, ok := e.(events.ContinuityBudget); ok {
			mu.Lock()
			budgets = append(budgets, b)
			mu.Unlock()
		}
	})

	stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = collectStream(t, stream)

	mu.Lock()
	defer mu.Unlock()
	if len(budgets) != 2 || budgets[1].EstTokens <= budgets[0].EstTokens {
		t.Fatalf("want input and retained response budgets: %+v", budgets)
	}
	if budgets[0].NumCtx != 8192 {
		t.Fatalf("NumCtx = %d, want 8192", budgets[0].NumCtx)
	}
}

// TestContinuitySinkIsInstanceScoped locks in the review fix: two active
// brains route continuity telemetry to their own pipelines' buses, never to
// each other's.
func TestContinuitySinkIsInstanceScoped(t *testing.T) {
	lines := []string{
		`{"model":"m","message":{"role":"assistant","content":"Hi."},"done":true,"done_reason":"stop"}`,
	}
	newBrain := func() *OllamaBrain {
		return &OllamaBrain{
			client:           ollamaStreamStub(t, lines),
			model:            "m",
			fullSystemPrompt: "system",
			cfg:              &config.Config{MaxHistory: 10, OllamaNumCtx: 8192, ContinuityIntegrityEnabled: true},
		}
	}
	a, b := newBrain(), newBrain()
	var mu sync.Mutex
	counts := map[string]int{}
	a.SetContinuitySink(func(events.Event) { mu.Lock(); counts["a"]++; mu.Unlock() })
	b.SetContinuitySink(func(events.Event) { mu.Lock(); counts["b"]++; mu.Unlock() })

	for _, o := range []*OllamaBrain{a, b} {
		stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		collectStream(t, stream)
	}

	mu.Lock()
	defer mu.Unlock()
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("both sinks should receive their own events, got %v", counts)
	}
	if counts["a"] != counts["b"] {
		t.Fatalf("identical turns should emit identically per instance, got %v", counts)
	}
}

// TestInfoSinkIsInstanceScoped locks in the same isolation as the continuity
// sink: two active brains route operator Info to their own pipelines' buses,
// never to each other's.
func TestInfoSinkIsInstanceScoped(t *testing.T) {
	a, b := &OllamaBrain{}, &OllamaBrain{}
	var mu sync.Mutex
	counts := map[string]int{}
	a.SetInfoSink(func(events.Info) { mu.Lock(); counts["a"]++; mu.Unlock() })
	b.SetInfoSink(func(events.Info) { mu.Lock(); counts["b"]++; mu.Unlock() })

	a.emitInfo(events.Info{Message: "from a"})
	b.emitInfo(events.Info{Message: "from b"})

	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Fatalf("each sink should receive only its own event, got %v", counts)
	}
}
