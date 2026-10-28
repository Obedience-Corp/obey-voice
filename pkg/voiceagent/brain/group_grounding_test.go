package brain

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/ollama/ollama/api"
)

func groupFixtureHistory() []Turn {
	return []Turn{
		{Role: "user", Content: "Let's demonstrate this group conversation."},
		{Role: "assistant", Content: "Let's compare ideas.", PersonaID: "private-santa-id", PersonaDisplayName: "Santa"},
		{Role: "assistant", Content: "I can contribute.", PersonaID: "private-obey-id", PersonaDisplayName: "Obey"},
	}
}
func groupFixtureRoster() []GroupParticipant {
	return []GroupParticipant{{Name: "Venessa"}, {Name: "Santa"}, {Name: "Obey"}, {Name: "Joker"}}
}
func assertGroupGrounding(t *testing.T, prompt string) {
	t.Helper()
	for _, want := range []string{"group chat already taking place inside Obey Voice", "human user", "You are Venessa", `Agents with replies recorded in this thread: ["Santa","Obey"]`, "not evidence that all have spoken", "Joker", "do not turn it into a setup", "later clarification"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q in request:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, `Agents with replies recorded in this thread: ["Santa","Obey","Joker"]`) {
		t.Fatal("catalog-only persona presented as having spoken")
	}
}

func TestOllamaGroupContextAfterCompactionAndRestore(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		var request api.ChatRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"We can compare ideas here."},"done":true}`+"\n")
		}))
		host, _ := url.Parse(srv.URL)
		cfg := &config.Config{ActivePersona: "private-venessa-id", AgentName: "Venessa", MaxHistory: 20, OllamaNumCtx: 8192}
		o := &OllamaBrain{client: api.NewClient(host, srv.Client()), model: "fixture", cfg: cfg}
		o.SetGroupParticipants(groupFixtureRoster())
		if err := o.ImportContinuity(ContinuitySnapshot{Canonical: groupFixtureHistory(), Working: []Turn{{Role: "user", Content: "We are demonstrating the current group chat."}}}); err != nil {
			t.Fatal(err)
		}
		if streaming {
			stream, err := o.ThinkStream(t.Context(), "I'm trying to demo this chat we're currently in.", StreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = drainStream(t, stream)
			if result := <-stream.Done; result.Err != nil {
				t.Fatal(result.Err)
			}
		} else if _, err := o.ThinkFull(t.Context(), "I'm trying to demo this chat we're currently in.", StreamOptions{}); err != nil {
			t.Fatal(err)
		}
		assertGroupGrounding(t, request.Messages[0].Content)
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "the current responder is Venessa") {
			t.Fatal("current reply lost the selected identity")
		}
		for _, turn := range o.History() {
			if strings.Contains(turn.Content, "Host conversation context") {
				t.Fatal("host instruction persisted as user content")
			}
		}
		if strings.Contains(request.Messages[0].Content, "private-") {
			t.Fatal("storage IDs leaked into participant context")
		}
		o.ClearHistory()
		if strings.Contains(o.conversationIdentityGuidance(), "Agents with replies recorded") {
			t.Fatal("previous thread participants survived clear")
		}
		srv.Close()
	}
}

func TestCLIGroupContextReachesNewAndResumedRequests(t *testing.T) {
	for _, name := range []string{"claude", "cursor", "grok", "codex"} {
		for _, resume := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/new", true: "/resume"}[resume], func(t *testing.T) {
				var provider Provider
				var request func() string
				var setSession func(string)
				switch name {
				case "claude":
					client := &fakeClaudeClient{fullResults: []*claude.ClaudeResult{{Result: "Let's demo the conversation."}}}
					b := newTestBrain(client)
					b.cfg.AgentName = "Venessa"
					provider = b
					request = func() string { return client.runPrompts[0] }
					setSession = func(s string) { b.sessionID = s }
				case "cursor":
					client := &fakeCursorClient{}
					b := newTestCursorBrain(client)
					b.cfg.AgentName = "Venessa"
					provider = b
					request = func() string { return client.prompts[0] }
					setSession = func(s string) { b.sessionID = s }
				case "grok":
					client := &fakeGrokClient{}
					b := newTestGrokBrain(client)
					b.cfg.AgentName = "Venessa"
					provider = b
					request = func() string { return client.runPrompts[0] }
					setSession = func(s string) { b.sessionID = s }
				case "codex":
					b, client := codexFixture(t)
					b.cfg.AgentName = "Venessa"
					b.cfg.MaxHistory = 20
					provider = b
					request = func() string { return client.requests[0].Prompt }
					setSession = func(s string) { b.sessionID = s }
				}
				provider.LoadHistory(groupFixtureHistory())
				setter, ok := provider.(interface{ SetGroupParticipants([]GroupParticipant) })
				if !ok {
					t.Fatal("provider ignores host roster")
				}
				roster := groupFixtureRoster()
				setter.SetGroupParticipants(roster)
				roster[3].Name = "mutated"
				if resume {
					setSession("existing-thread")
				}
				if _, err := provider.ThinkFull(t.Context(), "I'm demoing this chat.", StreamOptions{}); err != nil {
					t.Fatal(err)
				}
				assertGroupGrounding(t, request())
				if strings.Contains(request(), "mutated") {
					t.Fatal("roster aliases caller storage")
				}
			})
		}
	}
}
