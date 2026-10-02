package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAIStructuredDecisionAndNegativePaths(t *testing.T) {
	for _, mode := range []string{"work", "reply", "clarify", "refusal", "incomplete", "missing", "extra", "invalid_tier", "unavailable", "redirect", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Fatal("incorrect destination or auth")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["store"] != false || body["model"] != "explicit-test-model" || body["tools"] != nil {
					t.Fatalf("unexpected request options: %v", body)
				}
				format := body["text"].(map[string]any)["format"].(map[string]any)
				if format["strict"] != true || format["type"] != "json_schema" {
					t.Fatal("missing strict schema")
				}
				if mode == "cancel" {
					return nil, context.Canceled
				}
				content := `{"action":"work","tier":"simple"}`
				switch mode {
				case "reply", "clarify":
					content = `{"action":"` + mode + `","tier":"none"}`
				case "missing":
					content = `{"tier":"simple"}`
				case "extra":
					content = `{"action":"work","tier":"simple","command":"rm file"}`
				case "invalid_tier":
					content = `{"action":"work","tier":"none"}`
				case "unavailable":
					content = `{"action":"work","tier":"complex"}`
				}
				part := map[string]any{"type": "output_text", "text": content}
				if mode == "refusal" {
					part = map[string]any{"type": "refusal", "refusal": "No"}
				}
				status := "completed"
				if mode == "incomplete" {
					status = "incomplete"
				}
				raw, _ := json.Marshal(map[string]any{"model": "explicit-test-model", "status": status, "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{part}}}})
				code := 200
				header := make(http.Header)
				if mode == "redirect" {
					code = 302
					header.Set("Location", "https://example.com")
				}
				return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
			})}
			provider, err := NewOpenAI(OpenAIConfig{Model: "explicit-test-model", APIKey: "fixture-secret", Client: client})
			if err != nil {
				t.Fatal(err)
			}
			proposal, err := (WorkPolicy{Evaluator: provider, Mode: PolicyRouteChoice}).Decide(t.Context(), sampleInput())
			bad := mode == "refusal" || mode == "incomplete" || mode == "missing" || mode == "extra" || mode == "invalid_tier" || mode == "redirect" || mode == "cancel"
			if (err != nil) != bad || proposal.Accepted != (mode == "work") || calls != 1 {
				t.Fatalf("accepted=%v calls=%d err=%v", proposal.Accepted, calls, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
			if mode == "work" && (proposal.Evidence.Answers != nil || proposal.Evidence.Structured == nil) {
				t.Fatal("structured result became a probability claim")
			}
		})
	}
}
