package brain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

func TestContextBudgetDisplayIndependentOfIntegrity(t *testing.T) {
	for _, mode := range []string{"stream", "full", "transient", "failed full"} {
		t.Run(mode, func(t *testing.T) {
			reason := "stop"
			if mode == "failed full" {
				reason = "length"
			}
			line := fmt.Sprintf(`{"message":{"role":"assistant","content":%q},"done":true,"done_reason":%q}`, strings.Repeat("A complete sentence. ", 100), reason)
			o := &OllamaBrain{
				client: ollamaStreamStub(t, []string{line}), model: "m",
				cfg:              &config.Config{MaxHistory: 40, OllamaNumCtx: 8192, ContinuityIntegrityEnabled: false},
				fullSystemPrompt: "system", history: []api.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "remembered"}},
			}
			var budgets []events.ContinuityBudget
			o.SetContinuitySink(func(e events.Event) {
				if budget, ok := e.(events.ContinuityBudget); ok {
					budgets = append(budgets, budget)
				} else {
					t.Errorf("integrity is disabled; unexpected event %T", e)
				}
			})
			before := o.estimateTokens("")
			if mode == "stream" {
				stream, err := o.ThinkStream(t.Context(), "tell a story", StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				_, result := collectStream(t, stream)
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				if len(budgets) < 3 || budgets[1].EstTokens < budgets[0].EstTokens+64 {
					t.Fatalf("long generation did not publish intermediate usage: %+v", budgets)
				}
			} else {
				_, err := o.ThinkFull(t.Context(), "tell a story", StreamOptions{TransientHistory: mode == "transient"})
				if (err != nil) != (mode == "failed full") {
					t.Fatalf("unexpected result: %v", err)
				}
			}
			if len(budgets) < 2 {
				t.Fatalf("missing start/end usage: %+v", budgets)
			}
			final := budgets[len(budgets)-1]
			if final.EstTokens != o.estimateTokens("") || final.NumCtx != 8192 {
				t.Fatalf("budget does not match retained context: %+v", final)
			}
			if mode == "transient" || mode == "failed full" {
				if final.EstTokens != before {
					t.Fatalf("rolled-back history changed usage: before=%d final=%+v", before, final)
				}
			} else if final.EstTokens <= before {
				t.Fatalf("reply absent from final usage: before=%d final=%+v", before, final)
			}
		})
	}
}
