package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// jsonRouteFake answers native route rounds with a route tool call, schema
// requests with decide's text and everything else with answer. intercept, when
// set, sees each recorded request first and may answer it instead.
type jsonRouteFake struct {
	mu        sync.Mutex
	requests  []api.ChatRequest
	native    map[string]string
	decide    func(attempt int) string
	answer    string
	intercept func(w http.ResponseWriter, r *http.Request, req api.ChatRequest) bool
}

func (f *jsonRouteFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[{"name":"chat-model"}]}`)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		attempts := 0
		for _, sent := range f.requests {
			if len(sent.Format) > 0 {
				attempts++
			}
		}
		f.mu.Unlock()
		if f.intercept != nil && f.intercept(w, r, req) {
			return
		}
		msg := api.Message{Role: "assistant"}
		switch {
		case hasTool(req.Tools, "route"):
			args := api.NewToolCallFunctionArguments()
			for k, v := range f.native {
				args.Set(k, v)
			}
			msg.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}
		case len(req.Format) > 0:
			msg.Content = f.decide(attempts)
		default:
			msg.Content = f.answer
		}
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Model: req.Model, Message: msg, Done: true, DoneReason: "stop"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *jsonRouteFake) sent() []api.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func fixedDecision(raw string) func(int) string { return func(int) string { return raw } }

func jsonRouteTestBrain(t *testing.T, srv *httptest.Server) *OllamaBrain {
	t.Helper()
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &OllamaBrain{client: api.NewClient(base, srv.Client()), model: "fixture", fullSystemPrompt: "You are Samantha. Keep it short.",
		cfg: &config.Config{MaxHistory: 20, PromptsDir: t.TempDir()}}
}

type routedTurn struct {
	answer, thinking string
	result           StreamResult
	tools            []ToolResult
}

func runRoutedTurn(t *testing.T, o *OllamaBrain, input string, opts StreamOptions) routedTurn {
	t.Helper()
	var turn routedTurn
	var mu sync.Mutex
	opts.OnToolEnd = func(r ToolResult) {
		mu.Lock()
		turn.tools = append(turn.tools, r)
		mu.Unlock()
	}
	stream, err := o.ThinkStream(t.Context(), input, opts)
	if err != nil {
		t.Fatal(err)
	}
	turn.answer, turn.thinking = drainStream(t, stream)
	turn.result = <-stream.Done
	return turn
}

func noRouteExchangeInHistory(t *testing.T, o *OllamaBrain) {
	t.Helper()
	for _, m := range o.history {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Fatalf("route exchange written to history: %+v", m)
		}
	}
}

func spokenHistory(turns []Turn) []string {
	var out []string
	for _, turn := range turns {
		if turn.Role != "tool" && len(turn.ToolCalls) == 0 {
			out = append(out, turn.Role+": "+turn.Content)
		}
	}
	return out
}

func newPromptedOllama(t *testing.T, srv *httptest.Server, think, activePersona string) *OllamaBrain {
	t.Helper()
	home := t.TempDir()
	t.Cleanup(skills.SetUserHomeDirForTest(func() (string, error) { return home, nil }))
	o, err := NewOllama(&config.Config{OllamaHost: srv.URL, OllamaModel: "chat-model", OllamaNumCtx: 8192, OllamaNumBatch: 128,
		OllamaThink: think, Persona: "samantha", ActivePersona: activePersona, AgentName: "Samantha", UserDisplayName: "Lance",
		WorkDir: t.TempDir(), PromptsDir: t.TempDir(), MaxHistory: 20, VoiceToolsEnabled: true, VoiceRouterTools: JSONRouteSetting})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// json_route exists so the routing request and the reply share one prompt
// prefix. Everything Ollama renders ahead of the newest user text must be
// byte-identical: no tools in either request, the same system message and
// history, the same model, thinking setting and runner options. Only the
// final message differs, after the text both requests share. A persona turn
// carries skill context and the current-request reminder after the user's
// words; the shared text must include them.
func TestJSONRouteSharesThePromptPrefixWithTheReply(t *testing.T) {
	for _, tc := range []struct {
		name, think, persona string
		facts                []WorkerFact
	}{
		{"think off", "false", "", nil},
		{"think on", "true", "", nil},
		{"persona turn with active work", "false", "samantha", []WorkerFact{{ID: "job-1", Tier: config.WorkerTierSimple, State: "running"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			think := tc.think
			fake := &jsonRouteFake{decide: fixedDecision(`{"kind":"chat"}`), answer: "Fair enough, Lance."}
			o := newPromptedOllama(t, fake.server(t), think, tc.persona)
			opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: true, WorkerFacts: tc.facts}
			for _, input := range []string{"Hi there, how was your day?", "Tell me something about cats.", "What did I ask first?"} {
				before := len(fake.sent())
				turn := runRoutedTurn(t, o, input, opts)
				if turn.result.Err != nil || turn.answer != "Fair enough, Lance." {
					t.Fatalf("answer=%q err=%v", turn.answer, turn.result.Err)
				}
				sent := fake.sent()[before:]
				if len(sent) != 2 {
					t.Fatalf("want a route request and a reply, got %d requests", len(sent))
				}
				route, reply := sent[0], sent[1]
				if len(route.Tools) != 0 || len(reply.Tools) != 0 {
					t.Fatalf("tools offered: route=%d reply=%d", len(route.Tools), len(reply.Tools))
				}
				var schema, sentSchema bytes.Buffer
				_ = json.Compact(&schema, []byte(jsonOf(t, liveRouterTools()[0].Function.Parameters)))
				_ = json.Compact(&sentSchema, route.Format)
				if schema.String() != sentSchema.String() || len(reply.Format) != 0 {
					t.Fatalf("route format=%s reply format=%s", route.Format, reply.Format)
				}
				if route.Stream == nil || *route.Stream || route.Model != reply.Model ||
					jsonOf(t, route.KeepAlive) != jsonOf(t, reply.KeepAlive) || jsonOf(t, route.Think) != jsonOf(t, reply.Think) {
					t.Fatalf("request settings differ: route=%+v reply=%+v", route, reply)
				}
				if thinkEnabled(reply.Think) != (think == "true") {
					t.Fatalf("thinking setting not carried: %v", reply.Think)
				}
				options := func(o map[string]any) map[string]any {
					kept := map[string]any{}
					for k, v := range o {
						if k != "temperature" && k != "num_predict" {
							kept[k] = v
						}
					}
					return kept
				}
				if !reflect.DeepEqual(options(route.Options), options(reply.Options)) || len(options(reply.Options)) == 0 {
					t.Fatalf("runner options differ: route=%v reply=%v", route.Options, reply.Options)
				}
				wantCap := map[bool]int{false: routeDecisionTokens, true: jsonRouteThinkingTokens}[think == "true"]
				if route.Options["num_predict"] != float64(wantCap) {
					t.Fatalf("route num_predict=%v want %d", route.Options["num_predict"], wantCap)
				}
				last := len(reply.Messages) - 1
				if len(route.Messages) != len(reply.Messages) || reply.Messages[0].Role != "system" {
					t.Fatalf("message counts differ: route=%d reply=%d", len(route.Messages), len(reply.Messages))
				}
				shared := 0
				for i := 0; i < last; i++ {
					if jsonOf(t, route.Messages[i]) != jsonOf(t, reply.Messages[i]) {
						t.Fatalf("message %d differs", i)
					}
					shared += len(reply.Messages[i].Content)
				}
				routeLast, replyLast := route.Messages[last], reply.Messages[last]
				base, ok := strings.CutSuffix(replyLast.Content, "\n"+chatRouteAnswerInstruction)
				if routeLast.Role != "user" || replyLast.Role != "user" || !ok || !strings.HasPrefix(routeLast.Content, base+"\n\n") ||
					!strings.Contains(base, input) || jsonOf(t, routeLast.Images) != jsonOf(t, replyLast.Images) {
					t.Fatalf("final messages diverge before the shared text:\nroute=%q\nreply=%q", routeLast.Content, replyLast.Content)
				}
				if reminder := currentRequestReminder(o.cfg, opts); tc.persona != "" &&
					(reminder == "" || !strings.HasSuffix(base, reminder) || !strings.Contains(base, "<active_work>")) {
					t.Fatalf("shared text should end with the skill context and reminder: %q", base)
				}
				t.Logf("shared: %d messages, %d content bytes (system %d); reply re-reads %d bytes; route request adds %d bytes",
					last, shared+len(base), len(reply.Messages[0].Content), len(replyLast.Content)-len(base), len(routeLast.Content)-len(base))
			}
			noRouteExchangeInHistory(t, o)
		})
	}
}

// The cost json_route removes: under route_only the route round offers the
// route schema, which Ollama renders into the prompt beside the system text,
// and the reply adds the route exchange and an instruction inside the newest
// user message. The two requests share at most the system text.
func TestRouteOnlyRoundsDifferAheadOfTheHistory(t *testing.T) {
	fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, answer: "Fair enough, Lance."}
	o := newPromptedOllama(t, fake.server(t), "false", "")
	opts := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}
	runRoutedTurn(t, o, "Hi there, how was your day?", opts)
	before := len(fake.sent())
	if turn := runRoutedTurn(t, o, "Tell me something about cats.", opts); turn.result.Err != nil {
		t.Fatal(turn.result.Err)
	}
	sent := fake.sent()[before:]
	if len(sent) != 2 {
		t.Fatalf("want a route round and a reply, got %d requests", len(sent))
	}
	route, reply := sent[0], sent[1]
	if !onlyRouteTool(route.Tools) || len(reply.Tools) != 0 || len(route.Format) != 0 {
		t.Fatalf("route round tools=%d reply tools=%d", len(route.Tools), len(reply.Tools))
	}
	if len(reply.Messages) != len(route.Messages)+2 {
		t.Fatalf("reply should add the route exchange: route=%d reply=%d", len(route.Messages), len(reply.Messages))
	}
	newest := len(route.Messages) - 1
	if route.Messages[newest].Content == reply.Messages[newest].Content || !strings.Contains(reply.Messages[newest].Content, chatRouteAnswerInstruction) {
		t.Fatal("route_only reply should change the newest user message")
	}
	if !slices.ContainsFunc(route.Messages, func(m api.Message) bool { return m.Role == "tool" }) {
		t.Fatal("route_only keeps earlier route exchanges in history")
	}
	t.Logf("route_only: route tools %d bytes; system %d bytes shared; reply adds %d messages",
		len(jsonOf(t, route.Tools)), len(route.Messages[0].Content), len(reply.Messages)-len(route.Messages))
}

// Downstream handling must not depend on how the route was decided.
func TestJSONRouteOutcomesMatchTheRouteTool(t *testing.T) {
	tiers := &WorkerTierAvailability{Simple: true, Medium: true}
	for _, tc := range []struct {
		name     string
		decision map[string]string
		tiers    *WorkerTierAvailability
	}{
		{"chat", map[string]string{"kind": "chat"}, nil},
		{"spawn", map[string]string{"kind": "spawn", "goal": "check the forecast"}, nil},
		{"narrate", map[string]string{"kind": "narrate", "source": "notes.md"}, nil},
		{"tiered spawn", map[string]string{"kind": "spawn", "tier": "medium", "reason_code": "dependent_steps"}, tiers},
		{"tiered chat", map[string]string{"kind": "chat", "tier": "simple"}, tiers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var turns [2]routedTurn
			var history [2][]Turn
			for i, jsonMode := range []bool{false, true} {
				fake := &jsonRouteFake{native: tc.decision, decide: fixedDecision(jsonOf(t, tc.decision)), answer: "Sure thing."}
				o := jsonRouteTestBrain(t, fake.server(t))
				turns[i] = runRoutedTurn(t, o, "Can you check the forecast for Denver?",
					StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: jsonMode, WorkerTiers: tc.tiers})
				history[i] = o.History()
				if jsonMode {
					noRouteExchangeInHistory(t, o)
					if want := map[bool]int{true: 2, false: 1}[tc.decision["kind"] == "chat"]; len(fake.sent()) != want {
						t.Fatalf("requests=%d want %d", len(fake.sent()), want)
					}
				}
			}
			native, private := turns[0], turns[1]
			if native.answer != private.answer || native.result.Route != private.result.Route || native.result.RouteTier != private.result.RouteTier ||
				native.result.RouteReasonCode != private.result.RouteReasonCode || (native.result.Err == nil) != (private.result.Err == nil) {
				t.Fatalf("native=%+v %q\njson=%+v %q", native.result, native.answer, private.result, private.answer)
			}
			// route_only also keeps its route exchange; the conversation is the same.
			if a, b := spokenHistory(history[0]), spokenHistory(history[1]); !slices.Equal(a, b) {
				t.Fatalf("history differs:\nnative=%q\njson=%q", a, b)
			}
			if len(native.tools) != 1 || len(private.tools) != 1 || private.tools[0].Name != "route" ||
				private.tools[0].Summary != tc.decision["kind"] || private.tools[0].Outcome.Status != events.ToolOutcomeSucceeded ||
				private.tools[0].Outcome.Elapsed != private.tools[0].Elapsed {
				t.Fatalf("route events: native=%+v json=%+v", native.tools, private.tools)
			}
		})
	}
}

// A garbled decision is a missing route: one retry through the existing
// recovery call, then the existing error. Nothing is dispatched or spoken
// and each rejected attempt is visible as a failed route.
func TestJSONRouteRejectedDecisionFailsClosed(t *testing.T) {
	const garbled = "Sure, I'll check that for you."
	for _, tc := range []struct {
		name      string
		decide    func(int) string
		requests  int
		failed    int
		succeeded int
		answer    string
		err       string
	}{
		{"rejected twice", fixedDecision(garbled), 2, 2, 0, "", "recover missing route"},
		{"invalid kind then rejected", func(n int) string {
			return map[int]string{1: `{"kind":"maybe"}`, 2: `{"kind":"chat","answer":"hi"}`}[n]
		}, 2, 2, 0, "", "recover missing route"},
		{"recovered", func(n int) string { return map[int]string{1: garbled, 2: `{"kind":"chat"}`}[n] }, 3, 1, 1, "Fair enough.", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &jsonRouteFake{decide: tc.decide, answer: "Fair enough."}
			o := jsonRouteTestBrain(t, fake.server(t))
			turn := runRoutedTurn(t, o, "Can you check the forecast?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: true})
			sent := fake.sent()
			if len(sent) != tc.requests || turn.answer != tc.answer || turn.result.Route != "" {
				t.Fatalf("requests=%d answer=%q route=%q", len(sent), turn.answer, turn.result.Route)
			}
			if (tc.err == "") != (turn.result.Err == nil) || (tc.err != "" && !strings.Contains(turn.result.Err.Error(), tc.err)) {
				t.Fatalf("err=%v want %q", turn.result.Err, tc.err)
			}
			if !strings.Contains(sent[0].Messages[len(sent[0].Messages)-1].Content, "\nJSON schema: ") ||
				sent[1].Messages[0].Content == sent[0].Messages[0].Content || len(sent[1].Format) == 0 {
				t.Fatal("retry should be the existing recovery call")
			}
			if strings.Contains(turn.thinking, garbled) || strings.Contains(turn.answer, garbled) {
				t.Fatal("rejected decision text reached the user")
			}
			failed, succeeded := 0, 0
			for i, r := range turn.tools {
				if r.Name != "route" {
					t.Fatalf("unexpected tool event %+v", r)
				}
				switch r.Outcome.Status {
				case events.ToolOutcomeFailed:
					failed++
					if r.Err == "" || strings.Contains(r.Err, garbled) || (i == 0 && !strings.Contains(r.Err, "json_complete=")) {
						t.Fatalf("failed route event: %+v", r)
					}
				case events.ToolOutcomeSucceeded:
					succeeded++
					if r.Summary != "chat" || i != len(turn.tools)-1 {
						t.Fatalf("recovered route event: %+v", r)
					}
				}
			}
			if failed != tc.failed || succeeded != tc.succeeded || len(turn.tools) != tc.failed+tc.succeeded {
				t.Fatalf("route events failed=%d succeeded=%d want %d/%d: %+v", failed, succeeded, tc.failed, tc.succeeded, turn.tools)
			}
			noRouteExchangeInHistory(t, o)
		})
	}
}

// A short 200 body is a real transport failure for the Ollama client.
func truncateBody(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "10000")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

func jsonRouteOptions() StreamOptions {
	return StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: true}
}

func failedRouteEvent(t *testing.T, turn routedTurn, wantErr string) ToolResult {
	t.Helper()
	if len(turn.tools) != 1 || turn.tools[0].Name != "route" || turn.tools[0].Outcome == nil ||
		turn.tools[0].Outcome.Status != events.ToolOutcomeFailed || !strings.Contains(turn.tools[0].Err, wantErr) {
		t.Fatalf("want one failed route event mentioning %q, got %+v", wantErr, turn.tools)
	}
	return turn.tools[0]
}

// persona_routing_timeout cuts off a decision that never arrives, and the
// turn shows it as a failed route instead of waiting on the server.
func TestJSONRouteTimeoutBoundsTheRequest(t *testing.T) {
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &jsonRouteFake{answer: "Fair enough.", intercept: func(_ http.ResponseWriter, r *http.Request, req api.ChatRequest) bool {
		if len(req.Format) == 0 {
			return false
		}
		select {
		case <-r.Context().Done():
			canceled <- struct{}{}
		case <-release:
		case <-time.After(10 * time.Second):
		}
		return true
	}}
	srv := fake.server(t)
	t.Cleanup(func() { close(release) })
	o := jsonRouteTestBrain(t, srv)
	o.cfg.PersonaRoutingTimeout = 1
	start := time.Now()
	turn := runRoutedTurn(t, o, "Can you check the forecast?", jsonRouteOptions())
	took := time.Since(start)
	if !errors.Is(turn.result.Err, context.DeadlineExceeded) || !strings.Contains(turn.result.Err.Error(), "json route") || took > 5*time.Second {
		t.Fatalf("err=%v after %v", turn.result.Err, took)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the route request was never cut off")
	}
	if len(fake.sent()) != 1 || turn.answer != "" || turn.result.Route != "" {
		t.Fatalf("requests=%d answer=%q route=%q", len(fake.sent()), turn.answer, turn.result.Route)
	}
	// The request was held for the whole one-second timeout, so its measured
	// time cannot read as zero on any clock.
	if event := failedRouteEvent(t, turn, "deadline exceeded"); event.Elapsed < 500*time.Millisecond || event.Outcome.Elapsed != event.Elapsed {
		t.Fatalf("route event elapsed=%v outcome=%v", event.Elapsed, event.Outcome.Elapsed)
	}
	noRouteExchangeInHistory(t, o)
}

// A connection that drops during the decision fails the route visibly. The
// client's one outage retry stays inside the same route attempt.
func TestJSONRouteTransportErrorFailsTheRoute(t *testing.T) {
	fake := &jsonRouteFake{answer: "Fair enough.", intercept: func(w http.ResponseWriter, _ *http.Request, req api.ChatRequest) bool {
		if len(req.Format) == 0 {
			return false
		}
		truncateBody(w)
		return true
	}}
	o := jsonRouteTestBrain(t, fake.server(t))
	turn := runRoutedTurn(t, o, "Can you check the forecast?", jsonRouteOptions())
	var outage *ProviderOutage
	if !errors.As(turn.result.Err, &outage) || !strings.Contains(turn.result.Err.Error(), "json route") {
		t.Fatalf("err=%v", turn.result.Err)
	}
	sent := fake.sent()
	if len(sent) != 2 || len(sent[0].Format) == 0 || len(sent[1].Format) == 0 || turn.answer != "" {
		t.Fatalf("requests=%d answer=%q", len(sent), turn.answer)
	}
	failedRouteEvent(t, turn, "json route")
	noRouteExchangeInHistory(t, o)
}

// Retrying a failed json_route turn decides the route the same way, even when
// the host's retry call does not repeat the routing options.
func TestJSONRouteRetryKeepsJSONMode(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	fake := &jsonRouteFake{decide: fixedDecision(`{"kind":"chat"}`), answer: "Fair enough.",
		intercept: func(w http.ResponseWriter, _ *http.Request, _ api.ChatRequest) bool {
			if !failing.Load() {
				return false
			}
			truncateBody(w)
			return true
		}}
	o := jsonRouteTestBrain(t, fake.server(t))
	const input = "Can you check the forecast?"
	if first := runRoutedTurn(t, o, input, jsonRouteOptions()); first.result.Err == nil {
		t.Fatal("first turn should fail")
	}
	if retry, ok := o.FailedInputRetry(); !ok || retry.Input != input {
		t.Fatalf("retry=%+v ok=%v", retry, ok)
	}
	failing.Store(false)
	before := len(fake.sent())
	turn := runRoutedTurn(t, o, input, StreamOptions{RetryInput: true})
	sent := fake.sent()[before:]
	if turn.result.Err != nil || turn.answer != "Fair enough." || len(sent) != 2 {
		t.Fatalf("err=%v answer=%q requests=%d", turn.result.Err, turn.answer, len(sent))
	}
	first := sent[0]
	if len(first.Tools) != 0 || len(first.Format) == 0 || !strings.Contains(first.Messages[len(first.Messages)-1].Content, "\nJSON schema: ") {
		t.Fatalf("retried route request tools=%d format=%s", len(first.Tools), first.Format)
	}
	if len(turn.tools) != 1 || turn.tools[0].Outcome.Status != events.ToolOutcomeSucceeded || turn.tools[0].Summary != "chat" {
		t.Fatalf("route events: %+v", turn.tools)
	}
	noRouteExchangeInHistory(t, o)
}

func TestReportRouteCarriesTheMeasuredTime(t *testing.T) {
	var ended []ToolResult
	s := &toolSession{onEnd: func(r ToolResult) { ended = append(ended, r) }}
	const took = 1234 * time.Millisecond
	args := api.NewToolCallFunctionArguments()
	args.Set("kind", "spawn")
	s.reportRoute([]api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}, nil, took)
	s.reportRoute(nil, errRouteRejected, took)
	if len(ended) != 2 || ended[0].Summary != "spawn" || ended[0].Outcome.Status != events.ToolOutcomeSucceeded ||
		ended[1].Outcome.Status != events.ToolOutcomeFailed {
		t.Fatalf("events: %+v", ended)
	}
	for _, r := range ended {
		if r.Elapsed != took || r.Outcome.Elapsed != took {
			t.Fatalf("elapsed=%v outcome=%v want %v", r.Elapsed, r.Outcome.Elapsed, took)
		}
	}
}

// The decision cap keeps a smaller limit the request already carries, whatever
// numeric type holds it. Thinking gets a larger cap because Ollama counts the
// reasoning against num_predict.
func TestJSONRouteCapsTheDecisionOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		think bool
		limit any
		want  float64
	}{
		{"unset", false, nil, routeDecisionTokens},
		{"int kept", false, 100, 100},
		{"int64 kept", false, int64(100), 100},
		{"float64 kept", false, float64(100), 100},
		{"json.Number kept", false, json.Number("100"), 100},
		{"larger int capped", false, 4096, routeDecisionTokens},
		{"larger float64 capped", false, float64(4096), routeDecisionTokens},
		{"zero capped", false, 0, routeDecisionTokens},
		{"negative capped", false, int64(-1), routeDecisionTokens},
		{"non-numeric capped", false, "100", routeDecisionTokens},
		{"thinking unset", true, nil, jsonRouteThinkingTokens},
		{"thinking larger capped", true, 2048, jsonRouteThinkingTokens},
		{"thinking float64 kept", true, float64(512), 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &jsonRouteFake{decide: fixedDecision(`{"kind":"chat"}`)}
			o := jsonRouteTestBrain(t, fake.server(t))
			req := o.newChatRequest([]api.Message{{Role: "system", Content: "You are Samantha."}, {Role: "user", Content: "Hi"}}, liveRouterTools())
			req.Think = &api.ThinkValue{Value: tc.think}
			req.Options = map[string]any{}
			if tc.limit != nil {
				req.Options["num_predict"] = tc.limit
			}
			if err := o.chatJSONRoute(t.Context(), req, func(api.ChatResponse) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if got := fake.sent()[0].Options["num_predict"]; got != tc.want {
				t.Fatalf("num_predict=%v want %v", got, tc.want)
			}
			if req.Options["num_predict"] != tc.limit && tc.limit != nil {
				t.Fatal("the caller's options were changed")
			}
		})
	}
}

// Background-task tools need the native round; json_route applies only when
// the route schema is the whole tool list and the request ends with the user.
func TestJSONRouteKeepsTheNativeRoundWhenMoreIsOffered(t *testing.T) {
	runtime, err := delegation.New(context.Background(), &delegationClientStub{started: make(chan struct{})}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	fake := &jsonRouteFake{native: map[string]string{"kind": "chat"}, answer: "Sure."}
	o := jsonRouteTestBrain(t, fake.server(t))
	o.delegation = runtime
	turn := runRoutedTurn(t, o, "How are you?", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, RouteJSON: true})
	if turn.result.Err != nil || !hasTool(fake.sent()[0].Tools, "route") || !hasTool(fake.sent()[0].Tools, "agent_delegate") {
		t.Fatalf("err=%v first request tools=%d", turn.result.Err, len(fake.sent()[0].Tools))
	}
	user := []api.Message{{Role: "system"}, {Role: "user"}}
	if !jsonRouteEligible(liveRouterTools(), user) || jsonRouteEligible(liveRouterTools(), append(user, api.Message{Role: "tool"})) ||
		jsonRouteEligible(append(liveRouterTools(), delegationTools()...), user) {
		t.Fatal("eligibility")
	}
}

func TestJSONRouteSettingKeepsTheRouteOnlyPolicy(t *testing.T) {
	for _, raw := range []string{"json_route", " JSON_ROUTE "} {
		if !JSONRouteEnabled(raw) || NormalizeRouterToolsPolicy(raw) != RouterToolsRouteOnly {
			t.Fatalf("%q", raw)
		}
	}
	for _, raw := range []string{"", "route_only", "legacy"} {
		if JSONRouteEnabled(raw) {
			t.Fatalf("%q enabled json_route", raw)
		}
	}
	if jsonOf(t, RouterToolsForSession(NormalizeRouterToolsPolicy(JSONRouteSetting), nil)) != jsonOf(t, liveRouterTools()) {
		t.Fatal("json_route must keep the route_only tool session")
	}
}

// A json_route turn sends no tools, so its warmup must not either, or the
// cache it fills is never reused. With background-task tools the turn keeps
// the native round, and so does the warmup.
func TestWarmupFollowsJSONRoute(t *testing.T) {
	runtime, err := delegation.New(context.Background(), &delegationClientStub{started: make(chan struct{})}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	for _, tc := range []struct {
		name  string
		rt    *delegation.Runtime
		tools bool
	}{{"json_route", nil, false}, {"json_route with delegation", runtime, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var count int
			var bodies [][]byte
			o := &OllamaBrain{client: recordingStub(t, &count, &bodies), model: "m", fullSystemPrompt: "You are Samantha.", delegation: tc.rt,
				cfg: &config.Config{OllamaNumCtx: 8192, VoiceToolsEnabled: true, VoiceRouterTools: JSONRouteSetting}}
			o.Warmup(context.Background())
			if count != 1 || bytes.Contains(bodies[0], []byte(`"tools":`)) != tc.tools {
				t.Fatalf("count=%d body=%s", count, bodies[0])
			}
		})
	}
}
