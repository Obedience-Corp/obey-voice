package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// singleCallFake streams each scripted reply as one NDJSON frame per message,
// then a done frame. A reply's gate, when set, is waited on after its first
// frame so a test can prove that frame reached the caller first.
type singleCallFake struct {
	mu       sync.Mutex
	requests []api.ChatRequest
	replies  [][]api.Message
	gate     map[int]chan struct{}
}

func (f *singleCallFake) brain(t *testing.T) *OllamaBrain {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		n := len(f.requests) - 1
		f.mu.Unlock()
		if n >= len(f.replies) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		flusher, _ := w.(http.Flusher)
		enc := json.NewEncoder(w)
		if req.Stream != nil && !*req.Stream {
			var whole api.Message
			for _, msg := range f.replies[n] {
				whole.Content += msg.Content
				whole.ToolCalls = append(whole.ToolCalls, msg.ToolCalls...)
			}
			whole.Role = "assistant"
			_ = enc.Encode(api.ChatResponse{Message: whole, Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
			return
		}
		for i, msg := range f.replies[n] {
			msg.Role = "assistant"
			_ = enc.Encode(api.ChatResponse{Message: msg})
			flusher.Flush()
			if gate := f.gate[n]; i == 0 && gate != nil {
				select {
				case <-gate:
				case <-time.After(5 * time.Second):
					t.Error("first frame was not delivered before the reply finished")
				}
			}
		}
		_ = enc.Encode(api.ChatResponse{Message: api.Message{Role: "assistant"}, Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 10, EvalCount: 5}})
	}))
	t.Cleanup(srv.Close)
	host, _ := url.Parse(srv.URL)
	return &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", fullSystemPrompt: "You are Obey.",
		cfg: &config.Config{AgentName: "Obey", MaxHistory: 20, VoiceToolsEnabled: true, VoiceRouterTools: SingleCallSetting, PromptsDir: t.TempDir()}}
}

func (f *singleCallFake) sent() []api.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ChatRequest(nil), f.requests...)
}

func routeCall(kind string) api.ToolCall {
	return api.ToolCall{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": kind})}}
}

func recordPurposes(o *OllamaBrain) *[]string {
	var mu sync.Mutex
	purposes := &[]string{}
	o.SetModelRequestObserver(func(_ context.Context, req ModelRequest) func(ModelRequestResult) {
		mu.Lock()
		*purposes = append(*purposes, req.Purpose)
		mu.Unlock()
		return nil
	})
	return purposes
}

var singleCallTurn = StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}

func TestSingleCallChatIsOneStreamedRequest(t *testing.T) {
	gate := make(chan struct{})
	f := &singleCallFake{replies: [][]api.Message{{{Content: "Hey Lance, "}, {Content: "doing well."}}}, gate: map[int]chan struct{}{0: gate}}
	o := f.brain(t)
	purposes := recordPurposes(o)
	stream, err := o.ThinkStream(t.Context(), "How's it going?", singleCallTurn)
	if err != nil {
		t.Fatal(err)
	}
	if first := <-stream.Chunks; first != "Hey Lance, " {
		t.Fatalf("first chunk = %q", first)
	}
	close(gate)
	rest, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil || result.Route != "" {
		t.Fatalf("result = %+v", result)
	}
	if rest != "doing well." {
		t.Fatalf("rest = %q", rest)
	}
	sent := f.sent()
	if len(sent) != 1 || !onlyRouteTool(sent[0].Tools) || !strings.HasPrefix(sent[0].Tools[0].Function.Description, "Hand this user turn") {
		t.Fatalf("requests = %d, tools = %+v", len(sent), sent[0].Tools)
	}
	if strings.Join(*purposes, ",") != PurposeRouteReply {
		t.Fatalf("purposes = %v", *purposes)
	}
	if got := spokenHistory(o.History()); strings.Join(got, "|") != "user: How's it going?|assistant: Hey Lance, doing well." {
		t.Fatalf("history = %v", got)
	}
	noRouteExchangeInHistory(t, o)
}

func TestSingleCallHandOffMatchesTheRouteTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply []api.Message
		kind  string
		spoke string
	}{
		{"spawn", []api.Message{{ToolCalls: []api.ToolCall{routeCall("spawn")}}}, "spawn", ""},
		{"narrate", []api.Message{{ToolCalls: []api.ToolCall{routeCall("narrate")}}}, "narrate", ""},
		{"text then spawn", []api.Message{{Content: "Let me check. "}, {ToolCalls: []api.ToolCall{routeCall("spawn")}}}, "spawn", "Let me check. "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &singleCallFake{replies: [][]api.Message{tc.reply}}
			o := f.brain(t)
			turn := runRoutedTurn(t, o, "What's the weather in Denver?", singleCallTurn)
			if turn.result.Err != nil || turn.result.Route != tc.kind || len(f.sent()) != 1 {
				t.Fatalf("result = %+v, requests = %d", turn.result, len(f.sent()))
			}
			if turn.answer != tc.spoke {
				t.Fatalf("spoken = %q, want %q", turn.answer, tc.spoke)
			}
			// The routed history writer owns the user line, as under route_only.
			if len(o.History()) != 0 {
				t.Fatalf("history = %+v", o.History())
			}
		})
	}
}

func TestSingleCallTextBesideRouteChatIsTheReply(t *testing.T) {
	f := &singleCallFake{replies: [][]api.Message{{{Content: "Mornings are best slow."}, {ToolCalls: []api.ToolCall{routeCall("chat")}}}}}
	o := f.brain(t)
	turn := runRoutedTurn(t, o, "What makes a good morning?", singleCallTurn)
	if turn.result.Err != nil || turn.result.Route != "" || turn.answer != "Mornings are best slow." || len(f.sent()) != 1 {
		t.Fatalf("result = %+v, answer = %q, requests = %d", turn.result, turn.answer, len(f.sent()))
	}
	noRouteExchangeInHistory(t, o)
}

// route(chat) with nothing to say is today's routed chat: a tools-free reply.
func TestSingleCallBareRouteChatGetsAReplyRound(t *testing.T) {
	f := &singleCallFake{replies: [][]api.Message{{{ToolCalls: []api.ToolCall{routeCall("chat")}}}, {{Content: "Sure thing."}}}}
	o := f.brain(t)
	purposes := recordPurposes(o)
	turn := runRoutedTurn(t, o, "Hi there.", singleCallTurn)
	sent := f.sent()
	if turn.result.Err != nil || turn.answer != "Sure thing." || len(sent) != 2 || len(sent[1].Tools) != 0 {
		t.Fatalf("result = %+v, answer = %q, requests = %d", turn.result, turn.answer, len(sent))
	}
	if strings.Join(*purposes, ",") != PurposeRouteReply+","+PurposeReply {
		t.Fatalf("purposes = %v", *purposes)
	}
}

// An empty response and tool markup written as text both ask the existing
// recovery request for the route.
func TestSingleCallWithoutAnAnswerUsesRouteRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		first    []api.Message
		decision string
		route    string
		answer   string
		requests int
	}{
		{"empty then spawn", nil, `{"kind":"spawn"}`, "spawn", "", 2},
		{"empty then chat", []api.Message{{Content: "  "}}, `{"kind":"chat"}`, "", "Hello again.", 3},
		{"markup then spawn", []api.Message{{Content: `<tool_call>{"name": "route", "arguments": {"kind": "spawn"}}</tool_call>`}}, `{"kind":"spawn"}`, "spawn", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &singleCallFake{replies: [][]api.Message{tc.first, {{Content: tc.decision}}, {{Content: "Hello again."}}}}
			o := f.brain(t)
			purposes := recordPurposes(o)
			turn := runRoutedTurn(t, o, "Hello?", singleCallTurn)
			sent := f.sent()
			if turn.result.Err != nil || turn.result.Route != tc.route || strings.TrimSpace(turn.answer) != tc.answer || len(sent) != tc.requests {
				t.Fatalf("result = %+v, answer = %q, requests = %d", turn.result, turn.answer, len(sent))
			}
			if len(sent[1].Format) == 0 || (*purposes)[1] != PurposeRouteRecovery {
				t.Fatalf("second request is not the recovery request: %v", *purposes)
			}
		})
	}
}

func TestSingleCallMarkupAfterSpeechFailsWithoutDispatch(t *testing.T) {
	f := &singleCallFake{replies: [][]api.Message{{{Content: "Sure, one moment. "}, {Content: `<tool_call>{"name": "route"}`}}}}
	o := f.brain(t)
	turn := runRoutedTurn(t, o, "Look it up.", singleCallTurn)
	if turn.result.Err == nil || !turn.result.Recovered || turn.result.Route != "" || len(f.sent()) != 1 || strings.Contains(turn.answer, "tool_call") {
		t.Fatalf("result = %+v, answer = %q, requests = %d", turn.result, turn.answer, len(f.sent()))
	}
}

// With activated skills the routing request is sent without them, as under
// route_only, so its text selects chat and the reply round answers with them.
func TestSingleCallWithActivatedSkillsKeepsTheReplyRound(t *testing.T) {
	f := &singleCallFake{replies: [][]api.Message{{{Content: "Draft without the skill."}}, {{Content: "Planned answer."}}}}
	o := f.brain(t)
	o.skills = []skills.Skill{{Name: "planner", Description: "Plan the day", Body: "PLANNER_BODY"}}
	purposes := recordPurposes(o)
	stream, err := o.ThinkStream(WithActivatedSkills(t.Context(), []string{"planner"}), "Planner.", singleCallTurn)
	if err != nil {
		t.Fatal(err)
	}
	answer, thinking := drainStream(t, stream)
	result := <-stream.Done
	sent := f.sent()
	if result.Err != nil || answer != "Planned answer." || strings.Contains(thinking, "Draft") || len(sent) != 2 {
		t.Fatalf("result = %+v, answer = %q, requests = %d", result, answer, len(sent))
	}
	if strings.Contains(jsonOf(t, sent[0].Messages), "PLANNER_BODY") || !strings.Contains(jsonOf(t, sent[1].Messages), "PLANNER_BODY") || len(sent[1].Tools) != 0 {
		t.Fatal("the skill must reach the reply round and not the routing request")
	}
	if strings.Join(*purposes, ",") != PurposeRoute+","+PurposeReply {
		t.Fatalf("purposes = %v", *purposes)
	}
}

func TestSingleCallImageClaimStillRepairsBeforeSpeech(t *testing.T) {
	o, requests := imageClaimBrain(t, []api.Message{
		{Role: "assistant", Content: "I’ve attached them below."},
		{Role: "assistant", ToolCalls: []api.ToolCall{routeCall("spawn")}},
	})
	o.cfg.VoiceRouterTools = SingleCallSetting
	turn := runRoutedTurn(t, o, "Find a PNG of a dog and show it here.", singleCallTurn)
	if turn.result.Err != nil || turn.result.Route != "spawn" || turn.answer != "" || len(*requests) != 2 {
		t.Fatalf("result = %+v, answer = %q, requests = %d", turn.result, turn.answer, len(*requests))
	}
}

func TestSingleCallLeavesToolFreeTurnsAlone(t *testing.T) {
	f := &singleCallFake{replies: [][]api.Message{{{Content: "Hello."}}}}
	o := f.brain(t)
	purposes := recordPurposes(o)
	turn := runRoutedTurn(t, o, "Hi.", StreamOptions{RouterToolsPolicy: RouterToolsRouteOnly})
	if turn.result.Err != nil || turn.answer != "Hello." || len(f.sent()[0].Tools) != 0 || strings.Join(*purposes, ",") != PurposeReply {
		t.Fatalf("result = %+v, purposes = %v", turn.result, *purposes)
	}
}

// Only route_only with the setting uses single-call rounds; the default
// policies keep their schemas and purposes.
func TestSingleCallSettingSelection(t *testing.T) {
	for _, raw := range []string{"single_call", " SINGLE_CALL "} {
		if !SingleCallEnabled(raw) || NormalizeRouterToolsPolicy(raw) != RouterToolsRouteOnly || JSONRouteEnabled(raw) {
			t.Fatalf("%q", raw)
		}
	}
	for _, raw := range []string{"", "direct", "route_only", "json_route", "legacy"} {
		if SingleCallEnabled(raw) {
			t.Fatalf("%q enabled single_call", raw)
		}
	}
	o := &OllamaBrain{cfg: &config.Config{VoiceRouterTools: SingleCallSetting}}
	if sess := o.turnToolSession(StreamOptions{RouterToolsPolicy: RouterToolsDirect}); sess.singleCall {
		t.Fatal("direct turns never answer in a routing request")
	}
	o.cfg.VoiceRouterTools = "route_only"
	if sess := o.turnToolSession(singleCallTurn); sess.singleCall || jsonOf(t, sess.tools()) != jsonOf(t, liveRouterTools()) {
		t.Fatal("route_only must keep its route schema")
	}
	tiers := &WorkerTierAvailability{Simple: true}
	if got := singleCallRouterTools(tiers)[0].Function; len(got.Parameters.Required) != 2 || !strings.Contains(got.Description, "effort rubric") {
		t.Fatalf("tiered single-call schema = %+v", got)
	}
}

// The single-call tool surface is the measured wording: the route_only
// document with only its opening rule replaced.
func TestSingleCallToolSurfaceReplacesOnlyTheOpening(t *testing.T) {
	cfg := &config.Config{VoiceToolsEnabled: true, VoiceRouterTools: SingleCallSetting, PromptsDir: t.TempDir()}
	single := ToolSurfaceContext(cfg)
	cfg.VoiceRouterTools = "route_only"
	live := ToolSurfaceContext(cfg)
	const opening = "Tools: Live tool: route. First call route to select what happens; a spoken promise does not dispatch work. Use kind=chat for ordinary conversation, kind=spawn for actions and external information lookup, and kind=narrate to read aloud. Greetings, names, identity, and questions answerable from this conversation are kind=chat and need no worker."
	const measured = "Tools: Live tool: route. For ordinary conversation, answer directly in plain speech and do not call any tool; your answer is the reply. Call route with kind=spawn for actions and external information lookup, or kind=narrate to read aloud; a spoken promise does not dispatch work. Greetings, names, identity, and questions answerable from this conversation need no tool call and no worker."
	if !strings.Contains(live, opening) || single != strings.Replace(live, opening, measured, 1) {
		t.Fatalf("single-call surface:\n%s\nroute_only surface:\n%s", single, live)
	}
	if _, err := prompts.Describe(cfg.PromptsDir, prompts.KindSystem, "tool-surface-single-call"); err != nil {
		t.Fatal(err)
	}
}
