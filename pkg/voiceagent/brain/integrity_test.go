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
		"hello":     true,
		"hello.":    false,
		"really?":   false,
		"wow!":      false,
		"wait…":     false,
		"  trim  ":  true,
		"":          false,
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
	var got []events.Event
	SetContinuitySink(func(e events.Event) { got = append(got, e) })
	t.Cleanup(func() { SetContinuitySink(nil) })

	o := &OllamaBrain{
		client: ollamaStreamStub(t, lines),
		model:  "m",
		cfg:    &config.Config{MaxHistory: 10, ContinuityIntegrityEnabled: false},
	}
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
	SetContinuitySink(func(e events.Event) {
		if b, ok := e.(events.ContinuityBudget); ok {
			mu.Lock()
			budgets = append(budgets, b)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { SetContinuitySink(nil) })

	stream, err := o.ThinkStream(context.Background(), "hello", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = collectStream(t, stream)

	mu.Lock()
	defer mu.Unlock()
	if len(budgets) != 1 {
		t.Fatalf("budget events = %d, want 1", len(budgets))
	}
	if budgets[0].NumCtx != 8192 {
		t.Fatalf("NumCtx = %d, want 8192", budgets[0].NumCtx)
	}
}
