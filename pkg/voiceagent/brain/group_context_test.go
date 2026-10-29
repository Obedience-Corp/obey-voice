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
	"github.com/ollama/ollama/api"
)

func TestOllamaGroupProjectionUsesCompactWireLabels(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{
		ActivePersona: "samantha",
		AgentName:     "Samantha",
	}}
	o.SetGroupParticipants([]GroupParticipant{
		{Name: "Samantha", Aliases: []string{"sam"}},
		{Name: "Alex"},
	})
	o.SetSpeakerNames(testNames{"speaker-2": "Lance"})
	turns := []Turn{
		{Role: "user", Speaker: "speaker-2", Content: "hello"},
		{Role: "assistant", PersonaID: "alex", PersonaDisplayName: "Alex", Content: "checking"},
		{Role: "assistant", PersonaID: "samantha", PersonaDisplayName: "Samantha", Content: "I can help"},
		{Role: "tool", PersonaID: "alex", PersonaDisplayName: "Alex", ToolName: "lookup", Content: "cobalt"},
		{Role: "user", Speaker: "speaker-unknown", Content: "anyone there?"},
	}
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	messages := o.buildMessages("")
	if !strings.Contains(messages[0].Content, "This is a group chat") ||
		!strings.Contains(messages[0].Content, "Samantha") ||
		!strings.Contains(messages[0].Content, "Alex") {
		t.Fatalf("group system guidance = %q", messages[0].Content)
	}
	if got := messages[1].Content; got != "Lance user:\nhello" {
		t.Fatalf("user wire label = %q", got)
	}
	if messages[2].Role != "user" || messages[2].Content != "Alex agent:\nchecking" {
		t.Fatalf("shared assistant wire message = %+v", messages[2])
	}
	if messages[3].Role != "assistant" || messages[3].Content != "I can help" {
		t.Fatalf("own assistant was not native = %+v", messages[3])
	}
	if messages[4].Role != "user" || messages[4].Content != "Alex tool (lookup):\ncobalt" {
		t.Fatalf("shared tool wire message = %+v", messages[4])
	}
	if messages[5].Content != "Unknown user:\nanyone there?"+groupReplyInstruction(nil, "Samantha") {
		t.Fatalf("unknown user wire label = %q", messages[5].Content)
	}
}

func TestOllamaGroupRosterIsCopied(t *testing.T) {
	roster := []GroupParticipant{{Name: "Alex", Aliases: []string{"a"}}}
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "sam"}}
	o.SetGroupParticipants(roster)
	roster[0].Name = "Changed"
	roster[0].Aliases[0] = "changed"
	if got := o.groupParticipants[0].Name; got != "Alex" {
		t.Fatalf("roster name changed through caller slice: %q", got)
	}
	if got := o.groupParticipants[0].Aliases[0]; got != "a" {
		t.Fatalf("roster alias changed through caller slice: %q", got)
	}
}

func TestOllamaAgentContinuationDoesNotInventUserTurn(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"my reply"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{
		client: api.NewClient(base, http.DefaultClient),
		model:  "m",
		cfg:    &config.Config{ActivePersona: "samantha", AgentName: "Samantha", MaxHistory: 10},
	}
	o.SetGroupParticipants([]GroupParticipant{{Name: "Samantha"}, {Name: "Alex"}})
	o.LoadHistory([]Turn{
		{Role: "user", Content: "start", Speaker: "speaker-2"},
		{Role: "assistant", Content: "Alex's latest thought", PersonaID: "alex", PersonaDisplayName: "Alex"},
	})
	identity := &TurnIdentity{PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "r1"}
	stream, err := o.ThinkStream(t.Context(), "this must not be persisted", StreamOptions{
		AgentContinuation: true,
		Identity:          identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil {
		t.Fatalf("continuation result = %v", result.Err)
	}
	var request struct {
		Messages []api.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "this must not be persisted") {
			t.Fatalf("fabricated input reached Ollama: %+v", message)
		}
	}
	if !strings.Contains(request.Messages[0].Content, "latest agent is handing you the floor") {
		t.Fatalf("continuation system instruction missing: %q", request.Messages[0].Content)
	}
	history := o.CanonicalHistory()
	if len(history) != 3 || history[2].Role != "assistant" || history[2].ResponseID != "r1" {
		t.Fatalf("continuation canonical history = %+v", history)
	}
}

func TestOllamaThinkFullAgentContinuationDoesNotAcknowledgeBackground(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"full reply"},"done":true,"done_reason":"stop"}`+"\n")
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	o := &OllamaBrain{
		client: api.NewClient(base, http.DefaultClient),
		model:  "m",
		cfg:    &config.Config{ActivePersona: "samantha", AgentName: "Samantha", MaxHistory: 10},
	}
	o.SetGroupParticipants([]GroupParticipant{{Name: "Samantha"}, {Name: "Alex"}})
	o.LoadHistory([]Turn{{Role: "assistant", Content: "Alex said this", PersonaID: "alex", PersonaDisplayName: "Alex"}})
	const fabricated = "do not persist this input"
	if got, err := o.ThinkFull(t.Context(), fabricated, StreamOptions{
		AgentContinuation: true,
		Background:        true,
		Identity:          &TurnIdentity{PersonaID: "samantha", PersonaDisplayName: "Samantha"},
	}); err != nil {
		t.Fatal(err)
	} else if got != "full reply" {
		t.Fatalf("continuation reply = %q", got)
	}
	if strings.Contains(string(body), fabricated) ||
		strings.Contains(string(body), "newly arrived background task result") ||
		strings.Contains(string(body), "Report the newly arrived") {
		t.Fatalf("background instruction leaked into continuation request: %s", body)
	}
	history := o.CanonicalHistory()
	if len(history) != 2 || history[1].Role != "assistant" {
		t.Fatalf("full continuation history = %+v", history)
	}
}
