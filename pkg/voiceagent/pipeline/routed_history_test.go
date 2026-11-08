package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestRoutedWorkerRequestAndAcknowledgmentSurviveSessionReload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("routed acknowledgement called live model: %s", r.URL.Path)
			http.Error(w, "unexpected model call", 500)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", PromptsDir: t.TempDir(), WorkDir: t.TempDir(), VoiceWorkerProvider: "ollama", ActivePersona: "samantha"}
	b, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	NewAgency(p, jobs, &lifecycleWorker{reply: "done"}, cfg, nil).Wire()
	if err := p.EnableConversationWork("route-history", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	p.SetOutputMuted(true)
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "fixture")
	p.OnTurn = func() {
		if err := store.Save(sess, b.History()); err != nil {
			t.Error(err)
		}
	}
	var ready []events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { ready = append(ready, e) })
	response := 0
	p.AdmitInput = func(_ context.Context, _ string, _ bool) (ResponseAdmission, error) {
		response++
		return ResponseAdmission{Request: "Create a file called worker-proof.txt containing blue comet.", Envelope: events.ResponseEnvelope{SessionID: sess.ID, ConversationEpoch: "epoch", ResponseID: fmt.Sprintf("response-%d", response), PersonaID: "samantha", DisplayName: "Samantha", PromptHash: "prompt"}}, nil
	}
	const original = "Samantha, create a file called worker-proof.txt containing blue comet."
	for range 2 {
		if err := p.RunTurnTextMode(t.Context(), original); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Turns) != 4 {
		t.Fatalf("reloaded routed history = %+v, want two requests and their admission acknowledgments", loaded.Turns)
	}
	for i := range 2 {
		user, reply := loaded.Turns[2*i], loaded.Turns[2*i+1]
		if user.Role != "user" || user.Content != original || user.RoutedInput != "Create a file called worker-proof.txt containing blue comet." {
			t.Fatalf("original/routed input lost: %+v", user)
		}
		if reply.Role != "assistant" || reply.Content == "" || ready[i].Response != reply.Content || ready[i].Envelope == nil || ready[i].Envelope.ResponseID != fmt.Sprintf("response-%d", i+1) || reply.ResponseID != ready[i].Envelope.ResponseID || reply.PersonaID != "samantha" {
			t.Fatalf("routed acknowledgment lost its text or identity: reply=%+v ready=%+v", reply, ready[i])
		}
	}
	if !reflect.DeepEqual(loaded.Turns, b.History()) {
		t.Fatal("canonical history and disk differ")
	}
}

type routedProjectionBrain struct {
	*fakeBrain
	turns []brain.Turn
	err   error
}

func (b *routedProjectionBrain) History() []brain.Turn { return append([]brain.Turn(nil), b.turns...) }
func (b *routedProjectionBrain) ImportHistory(turns []brain.Turn) error {
	if b.err != nil {
		return b.err
	}
	b.turns = append([]brain.Turn(nil), turns...)
	return nil
}

func TestRoutedReplyPreservesExistingModelToolExchange(t *testing.T) {
	before := []brain.Turn{
		{Role: "user", Content: "prior request"},
		{Role: "assistant", Content: "prior reply", PersonaID: "santa", ResponseID: "prior"},
	}
	modelTurns := []brain.Turn{
		{Role: "user", Content: "Samantha, do the task", RoutedInput: "do the task", Speaker: "user-1"},
		{Role: "assistant", ToolCalls: []brain.TurnToolCall{{ID: "call-1", Name: "route", Arguments: `{"kind":"spawn"}`}}, PersonaID: "samantha", ResponseID: "new"},
		{Role: "tool", ToolName: "route", ToolCallID: "call-1", Content: "queued"},
	}
	b := &routedProjectionBrain{turns: append(append([]brain.Turn(nil), before...), modelTurns...)}
	p := &Pipeline{Brain: b}
	m := newTurnMetrics()
	m.originalInput, m.historyAtAdmission = modelTurns[0].Content, len(before)
	m.response = events.ResponseEnvelope{PersonaID: "samantha", DisplayName: "Samantha", ResponseID: "new"}
	if err := p.appendRoutedHistory(m, "do the task", "The request was cancelled."); err != nil {
		t.Fatal(err)
	}
	if len(b.turns) != len(before)+len(modelTurns)+1 || !reflect.DeepEqual(b.turns[:len(before)+len(modelTurns)], append(before, modelTurns...)) {
		t.Fatalf("model exchange changed or user duplicated: %+v", b.turns)
	}
	if got := b.turns[len(b.turns)-1]; got.Content != "The request was cancelled." || got.ResponseID != "new" {
		t.Fatalf("host reply = %+v", got)
	}
}

func TestRoutedHistoryFailureIsVisibleAndDoesNotReplaceHistory(t *testing.T) {
	failure := errors.New("projection refused")
	before := []brain.Turn{{Role: "user", Content: "saved request"}}
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{}, turns: before, err: failure}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "failed", PersonaID: "samantha"}}, nil
	}
	var gotError events.Error
	events.Subscribe(p.Events, func(e events.Error) { gotError = e })
	var ready events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { ready = e })
	err := p.RunTurnTextMode(t.Context(), "Create a file called worker-proof.txt containing blue comet.")
	if !errors.Is(err, failure) || gotError.Stage != "history" || !ready.Degraded {
		t.Fatalf("failure hidden: error=%v event=%+v ready=%+v", err, gotError, ready)
	}
	if !reflect.DeepEqual(b.turns, before) {
		t.Fatalf("failed projection changed canonical history: %+v", b.turns)
	}
}
