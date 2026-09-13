package brain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

func TestOllamaCanonicalHistoryKeepsIdentityBeyondProjectionTrim(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{MaxHistory: 2}}
	turns := []Turn{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "old", PersonaID: "santa", PersonaDisplayName: "Santa", PromptHash: "p1", ResponseID: "r1"}}
	o.LoadHistory(turns)
	o.appendMessage(api.Message{Role: "user", Content: "next"}, nil)
	o.appendMessage(api.Message{Role: "assistant", Content: "new"}, &TurnIdentity{PersonaID: "sam", PersonaDisplayName: "Samantha", PromptHash: "p2", ResponseID: "r2"})
	o.appendMessage(api.Message{Role: "user", Content: "third"}, nil)
	o.trimHistory()
	got := o.History()
	if len(got) != 5 || got[1].PersonaID != "santa" || got[3].ResponseID != "r2" {
		t.Fatalf("canonical history = %#v", got)
	}
}

func TestOllamaHistoryProjectionOmitsResponseIDs(t *testing.T) {
	o := &OllamaBrain{}
	turns := []Turn{{Role: "assistant", Content: "hello", PersonaID: "santa", PersonaDisplayName: "Santa", ResponseID: "internal-response-42"}}
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if len(o.history) != len(turns) {
		t.Fatalf("initial projection length = %d: %#v", len(o.history), o.history)
	}
	if got := o.history[0].Content; got != "Shared conversation — Santa said:\nhello" {
		t.Fatalf("projected history = %q", got)
	}
	if got := o.canonical[0].ResponseID; got != "internal-response-42" {
		t.Fatalf("canonical response id = %q, want retained metadata", got)
	}
}

func TestOllamaImportHistoryDoesNotResetProjectionLifecycle(t *testing.T) {
	o := &OllamaBrain{history: []api.Message{{Role: "assistant", Content: "live"}}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "restored"}}); err != nil {
		t.Fatal(err)
	}
	if len(o.history) != 1 || o.history[0].Role != "user" {
		t.Fatalf("projection = %#v", o.history)
	}
}

func TestOllamaPersonaProjectionSwitchesNativeAssistantOwnership(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha"}
	o := &OllamaBrain{cfg: cfg}
	turns := []Turn{
		{Role: "user", Content: "shared fact"},
		{Role: "assistant", Content: "Samantha answer", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "s-1"},
		{Role: "user", Content: "another fact"},
		{Role: "assistant", Content: "Santa answer", PersonaID: "santa", PersonaDisplayName: "Santa", ResponseID: "a-1"},
	}
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.history[1].Role != "assistant" || o.history[3].Role != "user" {
		t.Fatalf("Samantha projection = %#v", o.history)
	}
	cfg.ActivePersona = "santa"
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.history[1].Role != "user" || o.history[3].Role != "assistant" {
		t.Fatalf("Santa projection = %#v", o.history)
	}
	if got := o.history[1].Content; got != "Shared conversation — Samantha said:\nSamantha answer" {
		t.Fatalf("foreign assistant context = %q", got)
	}
	cfg.ActivePersona = "samantha"
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.history[1].Role != "assistant" || strings.Contains(o.history[1].Content, "Historical assistant") {
		t.Fatalf("returned persona was not native: %#v", o.history[1])
	}
	if !reflect.DeepEqual(o.CanonicalHistory(), turns) {
		t.Fatalf("canonical transcript changed: %#v", o.CanonicalHistory())
	}
}

func TestOllamaPersonaProjectionOwnsToolGroupsAndStripsLegacyHeaders(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha"}
	o := &OllamaBrain{cfg: cfg}
	turns := []Turn{
		{Role: "user", Content: "look up both"},
		{Role: "assistant", Content: "checking", PersonaID: "santa", PersonaDisplayName: "Santa", ToolCalls: []TurnToolCall{{ID: "c1", Name: "lookup", Arguments: `{}`}}},
		{Role: "tool", ToolName: "lookup", ToolCallID: "c1", Content: "Santa result"},
		{Role: "assistant", Content: "clean answer", PersonaID: "samantha", PersonaDisplayName: "Samantha", ToolCalls: []TurnToolCall{{ID: "c2", Name: "lookup", Arguments: `{}`}}},
		{Role: "tool", ToolName: "lookup", ToolCallID: "c2", Content: "Samantha result", PersonaID: "samantha", PersonaDisplayName: "Samantha"},
	}
	if err := o.ImportHistory(turns); err != nil {
		t.Fatal(err)
	}
	if o.history[1].Role != "user" || o.history[1].ToolCalls != nil || o.history[2].Role != "user" {
		t.Fatalf("foreign tool group was native: %#v", o.history)
	}
	if o.history[3].Role != "assistant" || len(o.history[3].ToolCalls) != 1 || o.history[4].Role != "tool" {
		t.Fatalf("own tool group was not native: %#v", o.history)
	}
	if strings.Contains(o.history[3].Content, "old-id") {
		t.Fatalf("legacy response ID leaked into projection: %q", o.history[3].Content)
	}
}

func TestOllamaReseedSummaryIsSharedContext(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha"}
	o := &OllamaBrain{cfg: cfg}
	original := []Turn{{Role: "user", Content: "fact"}, {Role: "assistant", Content: "reply", PersonaID: "samantha", PersonaDisplayName: "Samantha"}}
	if err := o.ImportHistory(original); err != nil {
		t.Fatal(err)
	}
	o.ReseedContinuity([]Turn{{Role: "assistant", Content: "summary"}, {Role: "user", Content: "next"}})
	if o.history[0].Role != "user" || !strings.Contains(o.history[0].Content, "Shared conversation context") {
		t.Fatalf("summary projection = %#v", o.history)
	}
	if !reflect.DeepEqual(o.CanonicalHistory(), original) {
		t.Fatalf("reseed changed canonical transcript: %#v", o.CanonicalHistory())
	}
}

func TestLegacyStackedHeadersAreNotReintroducedAfterResumeOrCompaction(t *testing.T) {
	legacy := "[Historical assistant: Obey response internal-1]\n[Historical assistant: Obey response internal-2]\nI am Obey."
	turns := []Turn{
		{Role: "user", Content: "remember cobalt", Speaker: "speaker-1"},
		{Role: "assistant", Content: legacy, PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "internal-3"},
		{Role: "user", Content: "Who are you?"},
		{Role: "assistant", Content: "I am Samantha.", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "internal-4"},
	}
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha", MaxHistory: 10}}
	o.LoadHistory(turns)
	view := o.ContextHistory()
	if view[0].Content != "remember cobalt" || view[0].Speaker != "speaker-1" || view[3].PersonaID != "samantha" {
		t.Fatalf("context lost user speaker or own persona: %+v", view)
	}
	o.ReseedContinuity(append([]Turn{{Role: "assistant", Content: "The user chose cobalt. Obey and Samantha introduced themselves."}}, view...))
	for _, message := range o.history {
		if strings.Contains(message.Content, "internal-") || strings.Contains(message.Content, "Historical assistant:") {
			t.Fatalf("legacy header/response id in model projection: %+v", message)
		}
		if message.Role == "assistant" && message.Content != "I am Samantha." {
			t.Fatalf("foreign speaker or summary became own assistant: %+v", message)
		}
	}
	if !reflect.DeepEqual(o.History(), turns) {
		t.Fatalf("canonical transcript changed: %+v", o.History())
	}
	// The same text supplied by the user is a quotation to answer about.
	o.LoadHistory([]Turn{{Role: "user", Content: legacy}})
	if o.history[0].Content != legacy {
		t.Fatal("user quotation was modified")
	}
}

func TestCompletionRetainsSpeakersWithoutHistoricalResponseIDs(t *testing.T) {
	turns := []Turn{{Role: "assistant", Content: "[Historical assistant: Obey response internal-response]\nI chose cobalt.", PersonaID: "obey", PersonaDisplayName: "Obey", ResponseID: "internal-response"}}
	text := completionPrompt(BuildCompletionRequest("What did Obey choose?", "", turns, nil))
	if !strings.Contains(text, "Obey said:") || !strings.Contains(text, "cobalt") || strings.Contains(text, "internal-response") || strings.Contains(text, "Historical assistant:") {
		t.Fatalf("completion attribution = %s", text)
	}
}

func TestBoundPersonaKeepsUnattributedLegacyToolExchangeAsSharedContext(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "check the fixture"},
		{Role: "assistant", Content: "I am an old agent.", ToolCalls: []TurnToolCall{{ID: "legacy-call", Name: "lookup", Arguments: `{}`}}},
		{Role: "tool", ToolName: "lookup", ToolCallID: "legacy-call", Content: "the code is cobalt"},
	}
	o := &OllamaBrain{cfg: &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}}
	o.LoadHistory(turns)
	for _, m := range o.history {
		if m.Role != "user" || len(m.ToolCalls) != 0 || m.ToolCallID != "" {
			t.Fatalf("unknown ownership assigned to Samantha or orphaned protocol: %+v", m)
		}
	}
	if !strings.Contains(o.history[1].Content, "lookup") || !strings.Contains(o.history[2].Content, "cobalt") {
		t.Fatalf("legacy tool context was lost: %+v", o.history)
	}
	if !reflect.DeepEqual(o.History(), turns) {
		t.Fatal("legacy shared transcript changed or acquired invented ownership")
	}
}
