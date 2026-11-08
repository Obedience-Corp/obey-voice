package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/ollama/ollama/api"
)

const currentRequestMarker = "responding to the latest human message"

func personaGreetingHistory() []Turn {
	return []Turn{
		{Role: "user", Content: "Inspect the newest planning item."},
		{Role: "assistant", Content: "I found the item and reported its findings.", PersonaID: "marigold-19", PersonaDisplayName: "Marigold-19"},
		{Role: "user", Content: "Prepare a structured proposal for that item."},
	}
}

// Exercise actual transport construction for both Ollama paths. The fixture
// proves the request boundary and canonical ownership, not model semantics.
func TestOllamaNewPersonaGreetingHasCurrentRequestBoundary(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "stream"}[streaming], func(t *testing.T) {
			var captured api.ChatRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
					return
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Hello, glad to join you."}, Done: true, DoneReason: "stop"})
			}))
			t.Cleanup(srv.Close)
			host, _ := url.Parse(srv.URL)
			cfg := &config.Config{ActivePersona: "juniper-42", AgentName: "Juniper-42", MaxHistory: 20, OllamaNumCtx: 8192}
			b := &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", cfg: cfg}
			before := personaGreetingHistory()
			if err := b.ImportHistory(before); err != nil {
				t.Fatal(err)
			}
			const greeting = "How's it going today?"
			opts := StreamOptions{Identity: &TurnIdentity{PersonaID: cfg.ActivePersona, PersonaDisplayName: cfg.AgentName, ResponseID: "greeting"}}
			if streaming {
				s, err := b.ThinkStream(t.Context(), greeting, opts)
				if err != nil {
					t.Fatal(err)
				}
				if _, result := collectStream(t, s); result.Err != nil {
					t.Fatal(result.Err)
				}
			} else if _, err := b.ThinkFull(t.Context(), greeting, opts); err != nil {
				t.Fatal(err)
			}
			if len(captured.Tools) != 0 || !strings.Contains(captured.Messages[0].Content, "You are Juniper-42, "+currentRequestMarker) {
				t.Fatalf("lost current persona or granted execution tools: %+v", captured)
			}
			last := captured.Messages[len(captured.Messages)-1]
			if last.Role != "user" || !strings.Contains(last.Content, greeting) || strings.Contains(last.Content, "Prepare a structured proposal") {
				t.Fatalf("old assignment became the current input: %+v", last)
			}
			assertCurrentRequestNotStored(t, b.History(), len(before), greeting)
		})
	}
}

func TestCLIPersonaRequestsKeepGuidanceOutOfCanonicalHistory(t *testing.T) {
	type fixture struct {
		provider Provider
		cfg      *config.Config
		prompt   func() string
	}
	cases := map[string]func(*testing.T) fixture{
		"claude": func(t *testing.T) fixture {
			f := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "Hello."}}}
			b := newTestBrain(f)
			return fixture{b, b.cfg, func() string { return f.runPrompts[0] }}
		},
		"grok": func(t *testing.T) fixture {
			f := &fakeGrokClient{}
			b := newTestGrokBrain(f)
			return fixture{b, b.cfg, func() string { return f.runPrompts[0] }}
		},
		"cursor": func(t *testing.T) fixture {
			f := &fakeCursorClient{results: []*cursor.AskResult{{Result: "Hello."}}}
			b := newTestCursorBrain(f)
			return fixture{b, b.cfg, func() string { return f.prompts[0] }}
		},
		"codex": func(t *testing.T) fixture {
			b, f := codexFixture(t)
			return fixture{b, b.cfg, func() string { return f.requests[0].Prompt }}
		},
		"muse-spark": func(t *testing.T) fixture {
			f := &fakeMuse{result: &muse.Result{Terminal: "completed", Text: "Hello."}}
			b := testMuse(f)
			return fixture{b, b.cfg, func() string { return f.prompt }}
		},
		"obey": func(t *testing.T) fixture {
			var prompt string
			b := obeyTestBrain(func(_ context.Context, p string, _ *obey.RunOptions, _ func(obey.Event) error) (*obey.Result, error) {
				prompt = p
				return &obey.Result{Text: "Hello."}, nil
			})
			return fixture{b, b.cfg, func() string { return prompt }}
		},
	}
	for name, create := range cases {
		t.Run(name, func(t *testing.T) {
			f := create(t)
			f.cfg.ActivePersona, f.cfg.AgentName = "juniper-42", "Juniper-42"
			before := personaGreetingHistory()
			f.provider.LoadHistory(before)
			const greeting = "How's it going today?"
			if _, err := f.provider.ThinkFull(t.Context(), greeting, StreamOptions{}); err != nil {
				t.Fatal(err)
			}
			prompt := f.prompt()
			if !strings.Contains(prompt, "You are Juniper-42, "+currentRequestMarker) || !strings.Contains(prompt, greeting) {
				t.Fatalf("new persona/current request boundary missing: %s", prompt)
			}
			for _, turn := range f.provider.History() {
				if strings.Contains(turn.Content, currentRequestMarker) {
					t.Fatalf("host guidance stored as conversation: %+v", turn)
				}
			}
		})
	}
}

func TestCurrentRequestBoundaryExcludesNonUserTurns(t *testing.T) {
	cfg := &config.Config{ActivePersona: "custom", AgentName: "Custom"}
	for name, opts := range map[string]StreamOptions{
		"background":   {Background: true},
		"continuation": {AgentContinuation: true},
		"compaction":   {OmitTurnInstruction: true},
		"transient":    {TransientHistory: true},
		"discussion":   {Discussion: &discussion.Directive{}},
	} {
		t.Run(name, func(t *testing.T) {
			if context, reminder := currentRequestContext(cfg, opts), currentRequestReminder(cfg, opts); context != "" || reminder != "" {
				t.Fatalf("internal turn told to answer a historical human message: %s %s", context, reminder)
			}
		})
	}
	worker := cfg.Clone()
	worker.IsWorker = true
	if currentRequestContext(worker, StreamOptions{}) != "" {
		t.Fatal("worker task brief replaced by conversational guidance")
	}
}

func assertCurrentRequestNotStored(t *testing.T, history []Turn, before int, input string) {
	t.Helper()
	if len(history) != before+2 || history[before].Content != input || history[before+1].PersonaID != "juniper-42" {
		t.Fatalf("request duplicated or response reassigned: %+v", history)
	}
	for _, turn := range history {
		if strings.Contains(turn.Content, currentRequestMarker) {
			t.Fatal("request-private guidance leaked into saved history")
		}
	}
}
