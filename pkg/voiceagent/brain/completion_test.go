package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestOllamaCompleteUsesPrivateStructuredRequest(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"content":"{\"answer\":\"I'll spin up a worker.\",\"updates\":[{\"result_id\":\"r1\",\"text\":\"72F\"}]}","prompt_eval_count":3,"eval_count":4},"done":true}`)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}, client: api.NewClient(endpoint, http.DefaultClient), model: "fake", fullSystemPrompt: "persona", history: []api.Message{{Role: "user", Content: "unchanged"}}}
	request := BuildCompletionRequest("", "", nil, []CompletionResult{{ResultID: "r1", Brief: "72F"}})
	before := len(o.history)
	envelope, err := o.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Answer != "" || len(envelope.Updates) != 1 {
		t.Fatalf("envelope=%+v", envelope)
	}
	if len(o.history) != before {
		t.Fatalf("completion mutated history: %+v", o.history)
	}
	if !strings.Contains(body, `"format":{"type":"object"`) || !strings.Contains(body, `"num_predict":256`) {
		t.Fatalf("request missing constraints: %s", body)
	}
	if !strings.Contains(body, "The current assistant persona is Samantha.") {
		t.Fatalf("worker completion lost current identity: %s", body)
	}
	if strings.Contains(body, `"tools":[`) {
		t.Fatalf("request enabled tools: %s", body)
	}
}

func TestBuildCompletionRequestBoundsResultsAndValidatesIDs(t *testing.T) {
	results := make([]CompletionResult, 5)
	for i := range results {
		results[i] = CompletionResult{ResultID: string(rune('a' + i)), Brief: strings.Repeat("x", 900)}
	}
	req := BuildCompletionRequest("input", "speaker", nil, results)
	if len(req.Results) == 0 || len(req.Results) > CompletionMaxResults {
		t.Fatalf("results=%d", len(req.Results))
	}
	if len(resultBytes(req.Results[0])) > CompletionResultTokens {
		t.Fatalf("projection exceeds bound: %d", len(resultBytes(req.Results[0])))
	}
	if err := (CompletionEnvelope{Answer: "ok", Updates: []CompletionUpdate{{ResultID: "a", Text: "done"}}}).Validate(req); err != nil {
		t.Fatal(err)
	}
	if err := (CompletionEnvelope{Answer: "ok", Updates: []CompletionUpdate{{ResultID: "missing", Text: "done"}}}).Validate(req); err == nil {
		t.Fatal("unknown update ID accepted")
	}
	if got := len(resultArrayBytes(req.Results)); got > CompletionResultTokens {
		t.Fatalf("serialized result array exceeds bound: %d", got)
	}
}

func TestBuildCompletionRequestBoundsEscapedMetadata(t *testing.T) {
	r := CompletionResult{ResultID: "r1", JobID: "job", Origin: strings.Repeat("origin\\\"", 40), Task: strings.Repeat("title\\\"", 40), Brief: strings.Repeat("brief\\\"", 200), SourceRef: strings.Repeat("source\\\"", 40), Limitations: strings.Repeat("limit\\\"", 40), DetailRef: "detail-1"}
	req := BuildCompletionRequest("", "", nil, []CompletionResult{r})
	if len(req.Results) != 1 || len(resultArrayBytes(req.Results)) > CompletionResultTokens {
		t.Fatalf("escaped projection not bounded: results=%d bytes=%d", len(req.Results), len(resultArrayBytes(req.Results)))
	}
	if _, err := json.Marshal(req.Results); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCompletionRequestDefersImmutableIdentityThatCannotFit(t *testing.T) {
	tooLarge := CompletionResult{ResultID: strings.Repeat("r", 1400), JobID: "oversized", DetailRef: "detail"}
	small := CompletionResult{ResultID: "small", JobID: "job", Brief: "kept"}
	req := BuildCompletionRequest("", "", nil, []CompletionResult{tooLarge, small})
	if len(req.Results) != 1 || req.Results[0].ResultID != "small" {
		t.Fatalf("oversized immutable identity was corrupted or starved later result: %+v", req.Results)
	}
}

func TestBuildCompletionRequestDenseFirstRecordUsesRemainingBudget(t *testing.T) {
	first := CompletionResult{ResultID: "first", JobID: strings.Repeat("j", 430), DetailRef: strings.Repeat("d", 230), Brief: "brief"}
	second := CompletionResult{ResultID: "second", JobID: "job", Brief: "later"}
	req := BuildCompletionRequest("", "", nil, []CompletionResult{first, second})
	if len(req.Results) == 0 || req.Results[0].ResultID != "first" {
		t.Fatalf("dense first record deferred unexpectedly: %+v", req.Results)
	}
	if len(resultArrayBytes(req.Results)) > CompletionResultTokens {
		t.Fatalf("projection exceeds bound")
	}
}

func TestCompletionEnvelopeRenderIncludesAnswerAndUpdatesOnce(t *testing.T) {
	req := BuildCompletionRequest("", "", nil, []CompletionResult{{ResultID: "r1"}, {ResultID: "r2"}})
	env := CompletionEnvelope{Answer: "answer", Updates: []CompletionUpdate{{ResultID: "r1", Text: "first"}, {ResultID: "r2", Text: "second"}}}
	if err := env.Validate(req); err != nil {
		t.Fatal(err)
	}
	if got := env.Render(); got != "answer\nfirst\nsecond" {
		t.Fatalf("render=%q", got)
	}
	for _, bad := range []CompletionEnvelope{{Answer: "", Updates: []CompletionUpdate{{ResultID: "r1", Text: ""}}}, {Answer: "x", Updates: []CompletionUpdate{{ResultID: "r1", Text: "a"}, {ResultID: "r1", Text: "b"}}}} {
		if bad.Validate(req) == nil {
			t.Fatalf("accepted malformed envelope: %+v", bad)
		}
	}
}

func TestHistoryAppenderQueuesCLIContextWithoutDroppingSession(t *testing.T) {
	g := &GrokBrain{sessionID: "remote-session"}
	if err := g.AppendAssistantResponse("A result arrived."); err != nil {
		t.Fatal(err)
	}
	if g.sessionID != "remote-session" || g.hostContext == "" || len(g.history) != 1 {
		t.Fatalf("appender lost session/context: %+v", g)
	}
	if !strings.Contains(g.buildPrompt(false), "A result arrived.") {
		t.Fatal("queued context missing from resumed prompt")
	}
}

func TestAppendHostTextRetainsNewestBoundedUTF8Context(t *testing.T) {
	old := strings.Repeat("old-prefix ", 1200)
	latest := "最新 worker update: retain-this-id"
	got := appendHostText(old, latest)
	if len(got) > 8192 || !utf8.ValidString(got) {
		t.Fatalf("host context invalid or over bound: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if !strings.Contains(got, latest) {
		t.Fatalf("newest update was lost: %q", got[len(got)-100:])
	}
	if !strings.HasPrefix(got, "[earlier host output truncated]\n") {
		t.Fatalf("missing truthful truncation marker")
	}
	first := appendHostText("", strings.Repeat("x", 9000))
	if len(first) > 8192 || !strings.Contains(first, "[earlier host output truncated]") {
		t.Fatalf("first oversized append was not bounded")
	}
}
