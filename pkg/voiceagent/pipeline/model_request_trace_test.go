package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

const (
	spanPrompt = "Tell me about the harbour."
	spanReply  = "The harbour is calm tonight."
)

// modelRequestServer answers like Ollama. The first routing request returns
// prose instead of a route, which forces the recovery request; the second
// routing request fails; replies wait for the caller when hang is set.
func modelRequestServer(t *testing.T, hang <-chan struct{}) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	routes := 0
	metrics := api.Metrics{LoadDuration: 3 * time.Millisecond, PromptEvalCount: 120, PromptEvalDuration: 40 * time.Millisecond, EvalCount: 7, EvalDuration: 25 * time.Millisecond}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		case "/api/chat":
		default:
			http.NotFound(w, r)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		enc := json.NewEncoder(w)
		switch {
		case req.Options["num_predict"] == float64(1):
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "h"}, Done: true, Metrics: metrics})
		case len(req.Tools) > 0:
			mu.Lock()
			routes++
			n := routes
			mu.Unlock()
			if n > 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"controlled route failure"}`))
				return
			}
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Let me think."}, Done: true, DoneReason: "stop", Metrics: metrics})
		case len(req.Format) > 0:
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: `{"kind":"chat"}`}, Done: true, DoneReason: "stop", Metrics: metrics})
		default:
			if hang != nil {
				_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "The "}})
				w.(http.Flusher).Flush()
				select {
				case <-hang:
				case <-r.Context().Done():
				}
				return
			}
			for word := range strings.FieldsSeq(spanReply) {
				_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: word + " "}})
			}
			_ = enc.Encode(api.ChatResponse{Done: true, DoneReason: "stop", Metrics: metrics})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func modelRequestPipeline(t *testing.T, server *httptest.Server, configure ...func(*config.Config)) (*Pipeline, *brain.OllamaBrain, func() []events.PipelineTrace) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{AgentName: "Harbour", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture:latest", PromptsDir: root, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192, VoiceToolsEnabled: true, VoiceRouterTools: "route_only"}
	for _, c := range configure {
		c(cfg)
	}
	provider, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var mu sync.Mutex
	var spans []events.PipelineTrace
	events.Subscribe(bus, func(e events.PipelineTrace) {
		if e.Span.Stage == "model_request" {
			mu.Lock()
			spans = append(spans, e)
			mu.Unlock()
		}
	})
	p := &Pipeline{Brain: provider, Events: bus, VoiceToolsEnabled: true}
	if err := p.BeginConversationIdentity("model-requests"); err != nil {
		t.Fatal(err)
	}
	return p, provider, func() []events.PipelineTrace {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.PipelineTrace(nil), spans...)
	}
}

type observedRequest struct {
	start, end events.PipelineTrace
	starts     int
	ends       int
}

// requestsByID pairs each request's start and end records, failing when a
// request ended before it started or reported either boundary twice.
func requestsByID(t *testing.T, spans []events.PipelineTrace) (map[string]*observedRequest, []string) {
	t.Helper()
	byID := map[string]*observedRequest{}
	var order []string
	for _, e := range spans {
		r := byID[e.Span.ID]
		if r == nil {
			r = &observedRequest{}
			byID[e.Span.ID] = r
			order = append(order, e.Span.ID)
		}
		switch e.Span.State {
		case "start":
			if r.ends > 0 {
				t.Fatalf("request %s ended before it started", e.Span.ID)
			}
			r.start = e
			r.starts++
		case "end":
			r.end = e
			r.ends++
		}
		if e.Span.Lane != "model" || e.Span.Request == nil || e.Span.Request.Provider != "ollama" || e.Span.Request.Model != "fixture:latest" {
			t.Fatalf("request span missing its description: %+v", e.Span)
		}
	}
	for id, r := range byID {
		if r.starts != 1 || r.ends != 1 {
			t.Fatalf("request %s has %d starts and %d ends", id, r.starts, r.ends)
		}
	}
	return byID, order
}

func TestModelRequestSpansFollowEachRequest(t *testing.T) {
	p, provider, spans := modelRequestPipeline(t, modelRequestServer(t, nil))
	p.ObserveModelRequests()
	provider.Warmup(t.Context())
	if err := p.RunTurnTextMode(t.Context(), spanPrompt); err != nil {
		t.Fatal(err)
	}
	if err := p.RunTurnTextMode(t.Context(), "And the lighthouse?"); err != nil {
		t.Fatal(err)
	}

	all := spans()
	byID, order := requestsByID(t, all)
	var purposes []string
	turns := map[string]bool{}
	for _, id := range order {
		r := byID[id]
		start, end := r.start.Span, r.end.Span
		purposes = append(purposes, start.Request.Purpose+":"+end.Status)
		if start.Request.FirstTokenMS != nil || start.Request.PromptTokens != nil || start.Request.PrefillMS != nil {
			t.Fatalf("start record claims measurements: %+v", start.Request)
		}
		if end.StartMS == nil || end.EndMS == nil || *end.EndMS < *end.StartMS {
			t.Fatalf("request %s has invalid wall time: %+v", id, end)
		}
		if r.end.Trace == nil || r.end.Trace.RuntimeID != p.ConversationScope().RuntimeEpoch {
			t.Fatalf("request %s lost its runtime: %+v", id, r.end.Trace)
		}
		if start.Request.Purpose == brain.PurposeWarmup {
			if r.end.Trace.TurnID != nil || end.ParentID != nil {
				t.Fatalf("warmup was attributed to a turn: %+v", r.end.Trace)
			}
			continue
		}
		if r.end.Trace.TurnID == nil || end.ParentID == nil || *end.ParentID != *r.end.Trace.TurnID {
			t.Fatalf("request %s is not a child of its turn: parent=%v trace=%+v", id, end.ParentID, r.end.Trace)
		}
		turns[*end.ParentID] = true
		if end.Status == "completed" {
			q := end.Request
			if q.PromptTokens == nil || *q.PromptTokens != 120 || q.OutputTokens == nil || *q.OutputTokens != 7 {
				t.Fatalf("request %s lost token counts: %+v", id, q)
			}
			if q.LoadMS == nil || *q.LoadMS != 3 || q.PrefillMS == nil || *q.PrefillMS != 40 || q.GenerationMS == nil || *q.GenerationMS != 25 {
				t.Fatalf("request %s lost provider durations: %+v", id, q)
			}
			if q.FirstTokenMS == nil || *q.FirstTokenMS < *end.StartMS || *q.FirstTokenMS > *end.EndMS {
				t.Fatalf("request %s first token outside its wall time: %+v", id, q)
			}
		}
	}
	want := "warmup:completed warmup:completed route:completed route_recovery:completed reply:completed route:failed"
	if got := strings.Join(purposes, " "); got != want {
		t.Fatalf("requests = %s\nwant       %s", got, want)
	}
	if len(turns) != 2 {
		t.Fatalf("requests span %d turns, want 2", len(turns))
	}
	failed := byID[order[len(order)-1]].end.Span.Request
	if failed.PromptTokens != nil || failed.PrefillMS != nil || failed.FirstTokenMS != nil {
		t.Fatalf("failed request reported measurements it never received: %+v", failed)
	}
	for _, e := range all {
		raw, err := json.Marshal(e.Span)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{spanPrompt, "harbour is calm", "Let me think", "lighthouse", `"kind"`} {
			if strings.Contains(string(raw), text) {
				t.Fatalf("span carries conversation text %q: %s", text, raw)
			}
		}
	}
}

func TestModelRequestSpanEndsCancelledWhenTheTurnIsCancelled(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	p, _, spans := modelRequestPipeline(t, modelRequestServer(t, hang))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events.Subscribe(p.Events, func(e events.ResponseDelta) { cancel() })
	_ = p.RunTurnTextMode(ctx, spanPrompt)

	byID, order := requestsByID(t, spans())
	last := byID[order[len(order)-1]].end.Span
	if last.Request.Purpose != brain.PurposeReply || last.Status != "cancelled" {
		t.Fatalf("interrupted reply ended %s/%s, want reply/cancelled", last.Request.Purpose, last.Status)
	}
	if last.Request.FirstTokenMS == nil {
		t.Fatal("first streamed token of the cancelled reply was not recorded")
	}
}

// statusServer routes every turn to chat, then answers the reply as the case
// requires. Requests it is told to hang wait for the caller to go away.
func statusServer(t *testing.T, recovery, reply string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		enc := json.NewEncoder(w)
		hang := func() {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
		switch {
		case len(req.Tools) > 0 && recovery != "":
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Hmm."}, Done: true, DoneReason: "stop"})
		case len(req.Tools) > 0:
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"function":{"name":"route","arguments":{"kind":"chat"}}}]},"done":true,"eval_count":1}` + "\n"))
		case len(req.Format) > 0 && recovery == "hang":
			hang()
		case len(req.Format) > 0:
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "{}"}, Done: true, DoneReason: "stop"})
		case reply == "hang":
			_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Well, "}})
			hang()
		default:
			for range 200 {
				_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "and so on "}})
			}
			_ = enc.Encode(api.ChatResponse{Done: true, DoneReason: "stop"})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Consumers must not read failed as a provider error: timeouts and the
// engine's own stops end failed too, and only a cancelled turn is cancelled.
func TestModelRequestEndStatusFollowsTheCause(t *testing.T) {
	for _, tc := range []struct {
		name, recovery, reply string
		configure             func(*config.Config)
		brainTimeout          time.Duration
		cancelOnDelta         bool
		purpose, status       string
	}{
		{name: "turn cancelled", reply: "hang", cancelOnDelta: true, purpose: brain.PurposeReply, status: "cancelled"},
		{name: "idle watchdog", reply: "hang", brainTimeout: 150 * time.Millisecond, purpose: brain.PurposeReply, status: "failed"},
		{name: "routing deadline", recovery: "hang", configure: func(c *config.Config) { c.PersonaRoutingTimeout = 1 }, purpose: brain.PurposeRouteRecovery, status: "failed"},
		{name: "response budget", configure: func(c *config.Config) { c.OllamaResponseTokens = 50 }, purpose: brain.PurposeReply, status: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var configure []func(*config.Config)
			if tc.configure != nil {
				configure = append(configure, tc.configure)
			}
			p, _, spans := modelRequestPipeline(t, statusServer(t, tc.recovery, tc.reply), configure...)
			p.BrainTurnTimeout = tc.brainTimeout
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if tc.cancelOnDelta {
				events.Subscribe(p.Events, func(events.ResponseDelta) { cancel() })
			}
			_ = p.RunTurnTextMode(ctx, spanPrompt)

			byID, order := requestsByID(t, spans())
			last := byID[order[len(order)-1]].end.Span
			if last.Request.Purpose != tc.purpose || last.Status != tc.status {
				t.Fatalf("last request ended %s/%s, want %s/%s", last.Request.Purpose, last.Status, tc.purpose, tc.status)
			}
		})
	}
}

// Each handoff check reads the reply that just finished, so its addressee
// request belongs to that turn, not to no turn or the turn before it.
func TestHandoffAddresseeRequestBelongsToTheFinishedTurn(t *testing.T) {
	p, provider, spans := modelRequestPipeline(t, statusServer(t, "", ""), func(c *config.Config) { c.VoiceToolsEnabled = false })
	p.VoiceToolsEnabled = false
	var turns []string
	var mu sync.Mutex
	events.Subscribe(p.Events, func(e events.PipelineTrace) {
		if e.Span.Stage == "turn" && e.Span.State == "start" {
			mu.Lock()
			turns = append(turns, e.Span.ID)
			mu.Unlock()
		}
	})
	admission := func(persona string) ResponseAdmission {
		return ResponseAdmission{Request: spanPrompt, Envelope: events.ResponseEnvelope{PersonaID: persona, ResponseID: "response-" + persona}}
	}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) { return admission("a"), nil }
	next := []string{"b", "c"}
	p.RouteHandoff = func(ctx context.Context, turn brain.Turn, _ []string) (HandoffProposal, bool, error) {
		_, _ = provider.ResolveAddressee(ctx, brain.AddressRequest{Account: "agent", Text: turn.Content})
		if len(next) == 0 {
			return HandoffProposal{}, false, nil
		}
		persona := next[0]
		next = next[1:]
		return HandoffProposal{PersonaID: persona, Commit: func(context.Context) (ResponseAdmission, error) { return admission(persona), nil }}, true, nil
	}
	if err := p.RunTurnTextMode(t.Context(), spanPrompt); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	byID, order := requestsByID(t, spans())
	var parents []string
	for _, id := range order {
		end := byID[id].end.Span
		if end.Request.Purpose != brain.PurposeOther {
			continue
		}
		parent := "none"
		if end.ParentID != nil {
			parent = *end.ParentID
		}
		parents = append(parents, parent)
	}
	if len(turns) != 3 || strings.Join(parents, " ") != strings.Join(turns, " ") {
		t.Fatalf("addressee parents = %v, want the turns %v in order", parents, turns)
	}
}

func TestCompactionRequestsHaveTheirOwnPurpose(t *testing.T) {
	p, _, spans := modelRequestPipeline(t, statusServer(t, "", ""), func(c *config.Config) { c.VoiceToolsEnabled = false })
	p.VoiceToolsEnabled = false
	p.CompactPrompt = "Summarize the conversation."
	if err := p.RunTurnTextMode(t.Context(), spanPrompt); err != nil {
		t.Fatal(err)
	}
	_ = p.CompactConversation(t.Context())
	byID, order := requestsByID(t, spans())
	last := byID[order[len(order)-1]].end
	if last.Span.Request.Purpose != brain.PurposeCompaction || last.Trace.TurnID != nil {
		t.Fatalf("compaction request = %s in turn %v", last.Span.Request.Purpose, last.Trace.TurnID)
	}
}
