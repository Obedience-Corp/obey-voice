package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type recordedTranscript struct {
	mu    sync.Mutex
	turns []brain.Turn
}

func (r *recordedTranscript) Turns() []brain.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.turns)
}

func (r *recordedTranscript) Append(_ string, turns ...brain.Turn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turns = append(r.turns, turns...)
	return nil
}

func (r *recordedTranscript) SetReplyMedia(string, []chatmedia.Ref) error { return nil }

// stallingBrain delivers one chunk, then holds the request open until it is
// canceled, as a CLI provider does when the user interrupts it.
type stallingBrain struct {
	fakeBrain
	partial string
}

func (b *stallingBrain) ThinkStream(ctx context.Context, _ string, _ brain.StreamOptions) (*brain.Stream, error) {
	out, done := make(chan string, 1), make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		out <- b.partial
		<-ctx.Done()
		done <- brain.StreamResult{Err: ctx.Err()}
	}()
	return &brain.Stream{Chunks: out, Done: done}, nil
}

func admitAs(responseID string, request func(string) string) func(context.Context, string, bool) (ResponseAdmission, error) {
	return func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: request(input), Envelope: events.ResponseEnvelope{SessionID: "s", ResponseID: responseID, PersonaID: "mira", DisplayName: "Mira", PromptHash: "mira-prompt"}}, nil
	}
}

func TestHostTranscriptRecordsProviderReplyUnderAdmittedIdentity(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{chunks: []string{"The door ", "is teal."}}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(string) string { return "what color is the door?" })
	var ready events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { ready = e })
	if err := p.RunTurnTextMode(t.Context(), "Mira, what color is the door?"); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 {
		t.Fatalf("transcript = %+v", turns)
	}
	user, reply := turns[0], turns[1]
	if user.Role != "user" || user.Content != "Mira, what color is the door?" || user.RoutedInput != "what color is the door?" {
		t.Fatalf("user turn = %+v", user)
	}
	if ready.Envelope == nil || reply.Role != "assistant" || reply.Content != ready.Response || reply.ResponseID != ready.Envelope.ResponseID ||
		reply.PersonaID != "mira" || reply.PersonaDisplayName != "Mira" || reply.PromptHash != "mira-prompt" || reply.Disposition != "" {
		t.Fatalf("reply = %+v, ready = %+v", reply, ready)
	}
}

func TestHostTranscriptRecordsInterruptedReplyBeforeTheTurnReturns(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &stallingBrain{partial: "The vault code is"}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	if err := p.RunTurnTextMode(ctx, "What is the vault code?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "What is the vault code?" || turns[1].Content != b.partial || turns[1].Disposition != "interrupted" || turns[1].ResponseID != "response-1" {
		t.Fatalf("interrupted transcript = %+v", turns)
	}

	p.Brain = &fakeBrain{chunks: []string{"It is cobalt."}}
	p.AdmitInput = admitAs("response-2", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Try again."); err != nil {
		t.Fatal(err)
	}
	turns = transcript.Turns()
	if len(turns) != 4 || turns[2].Content != "Try again." || turns[3].Content != "It is cobalt." || turns[3].Disposition != "" || turns[3].ResponseID != "response-2" {
		t.Fatalf("transcript after the interruption = %+v", turns)
	}
}

func TestHostTranscriptLeavesSharedHistoryProvidersAlone(t *testing.T) {
	transcript := &recordedTranscript{}
	b := &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Kept by the provider."}}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("response-1", func(input string) string { return input })
	if err := p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
		t.Fatal(err)
	}
	if turns := transcript.Turns(); len(turns) != 0 {
		t.Fatalf("host recorded a provider that keeps shared history: %+v", turns)
	}
}

func TestHostTranscriptRecordsLocalAcknowledgement(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{Brain: &fakeBrain{}, Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: "", LocalReply: "Mira here.", Envelope: events.ResponseEnvelope{ResponseID: "ack", PersonaID: "mira", DisplayName: "Mira"}}, nil
	}
	if err := p.RunTurnTextMode(t.Context(), "Mira."); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "Mira." || turns[1].Content != "Mira here." || turns[1].ResponseID != "ack" || turns[1].PersonaID != "mira" {
		t.Fatalf("acknowledgement transcript = %+v", turns)
	}
}

func TestHostTranscriptRecordsVoiceTurns(t *testing.T) {
	transcript := &recordedTranscript{}
	p := &Pipeline{STT: &fakeSTT{text: "what's for dinner"}, Brain: &fakeBrain{chunks: []string{"Soup tonight."}}, VAD: &fakeVAD{}, Capture: newFakeCapture(), Events: events.NewBus(), Transcript: transcript}
	p.AdmitInput = admitAs("voice-1", func(input string) string { return input })
	if _, err := p.RunTurn(t.Context()); err != nil {
		t.Fatal(err)
	}
	turns := transcript.Turns()
	if len(turns) != 2 || turns[0].Content != "what's for dinner" || turns[1].Content != "Soup tonight." || turns[1].ResponseID != "voice-1" || turns[1].Disposition != "" {
		t.Fatalf("voice transcript = %+v", turns)
	}

	p.Brain = &stallingBrain{partial: "Soup, and then"}
	p.AdmitInput = admitAs("voice-2", func(input string) string { return input })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	events.Subscribe(p.Events, func(events.ResponseDelta) { once.Do(cancel) })
	_, _ = p.RunTurn(ctx)
	turns = transcript.Turns()
	if len(turns) != 4 || turns[3].Content != "Soup, and then" || turns[3].Disposition != "interrupted" || turns[3].ResponseID != "voice-2" {
		t.Fatalf("interrupted voice transcript = %+v", turns)
	}
}

func TestHostTranscriptRecordsRoutedReplies(t *testing.T) {
	envelope := events.ResponseEnvelope{ResponseID: "routed", PersonaID: "mira", DisplayName: "Mira"}
	t.Run("deterministic route records the request", func(t *testing.T) {
		transcript := &recordedTranscript{turns: []brain.Turn{{Role: "user", Content: "earlier"}}}
		p := &Pipeline{Brain: &fakeBrain{}, Transcript: transcript}
		m := newTurnMetrics()
		m.originalInput, m.historyAtAdmission, m.response = "Mira, start a worker on the report.", 1, envelope
		if err := p.appendRoutedHistory(m, "start a worker on the report.", "Started."); err != nil {
			t.Fatal(err)
		}
		turns := transcript.Turns()
		if len(turns) != 3 || turns[1].Content != m.originalInput || turns[1].RoutedInput != "start a worker on the report." || turns[2].Content != "Started." || turns[2].ResponseID != "routed" {
			t.Fatalf("transcript = %+v", turns)
		}
	})
	t.Run("route after a model reply adds only the reply", func(t *testing.T) {
		transcript := &recordedTranscript{turns: []brain.Turn{{Role: "user", Content: "Do the report."}, {Role: "assistant", Content: "I'll start that.", ResponseID: "routed"}}}
		p := &Pipeline{Brain: &fakeBrain{}, Transcript: transcript}
		m := newTurnMetrics()
		m.originalInput, m.historyAtAdmission, m.response = "Do the report.", 0, envelope
		if err := p.appendRoutedHistory(m, "Do the report.", "Started."); err != nil {
			t.Fatal(err)
		}
		if turns := transcript.Turns(); len(turns) != 3 || turns[2].Content != "Started." || turns[2].PersonaID != "mira" {
			t.Fatalf("transcript = %+v", turns)
		}
	})
}
