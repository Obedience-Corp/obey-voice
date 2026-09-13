package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type continuationBrain struct {
	mu      sync.Mutex
	inputs  []string
	options []brain.StreamOptions
	chunks  []string
	result  brain.StreamResult
}

func (b *continuationBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.inputs = append(b.inputs, input)
	b.options = append(b.options, opts)
	b.mu.Unlock()
	chunks := make(chan string, len(b.chunks))
	done := make(chan brain.StreamResult, 1)
	for _, chunk := range b.chunks {
		chunks <- chunk
	}
	close(chunks)
	go func() {
		select {
		case <-ctx.Done():
			done <- brain.StreamResult{Err: ctx.Err()}
		default:
			done <- b.result
		}
		close(done)
	}()
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (*continuationBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (*continuationBrain) ClearHistory()            {}
func (*continuationBrain) History() []brain.Turn    { return nil }
func (*continuationBrain) LoadHistory([]brain.Turn) {}

func (b *continuationBrain) calls() (inputs []string, options []brain.StreamOptions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.inputs...), append([]brain.StreamOptions(nil), b.options...)
}

func admission(persona string) ResponseAdmission {
	return ResponseAdmission{
		Request: "ignored continuation input",
		Envelope: events.ResponseEnvelope{
			PersonaID:   persona,
			ResponseID:  "response-" + persona,
			DisplayName: persona,
		},
	}
}

func TestTextContinuationUsesImmutableAdmissionAndNoFakeUser(t *testing.T) {
	b := &continuationBrain{chunks: []string{"First answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	var visited [][]string
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		result := admission("one")
		result.Request = input
		return result, nil
	}
	p.RouteHandoff = func(_ context.Context, completed brain.Turn, prior []string) (ResponseAdmission, bool, error) {
		visited = append(visited, append([]string(nil), prior...))
		if len(visited) == 1 {
			if completed.PersonaID != "one" {
				t.Fatalf("completed persona = %q, want one", completed.PersonaID)
			}
			return admission("two"), true, nil
		}
		return ResponseAdmission{}, false, nil
	}

	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	inputs, options := b.calls()
	if len(inputs) != 2 || inputs[0] != "hello" || inputs[1] != "ignored continuation input" {
		t.Fatalf("brain inputs = %q, want initial input plus admitted continuation request", inputs)
	}
	if !options[1].AgentContinuation || options[1].ToolsEnabled || options[1].Background {
		t.Fatalf("continuation options = %+v, want continuation=true tools=false background=false", options[1])
	}
	if len(visited) != 2 || len(visited[0]) != 1 || visited[0][0] != "one" {
		t.Fatalf("handoff visited identities = %v, want initial identity before first callback", visited)
	}
}

func TestContinuationLimitIsThreeAdditionalResponses(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("one"), nil
	}
	var handoffs int
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		handoffs++
		return admission(string(rune('a' + handoffs + 1))), true, nil
	}

	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if handoffs != maxAgentContinuations {
		t.Fatalf("handoffs = %d, want %d", handoffs, maxAgentContinuations)
	}
	inputs, _ := b.calls()
	if len(inputs) != maxAgentContinuations+1 {
		t.Fatalf("brain calls = %d, want %d", len(inputs), maxAgentContinuations+1)
	}
}

func TestContinuationRefusesRepeatedPersona(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("one"), nil
	}
	var handoffs int
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		handoffs++
		return admission("one"), true, nil
	}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if handoffs != 1 {
		t.Fatalf("handoff callback count = %d, want 1 repeated target refused", handoffs)
	}
	inputs, _ := b.calls()
	if len(inputs) != 1 {
		t.Fatalf("brain calls = %d, want no repeated continuation", len(inputs))
	}
}

func TestContinuationSkipsDegradedResponses(t *testing.T) {
	b := &continuationBrain{
		chunks: []string{brain.RecoveryReply},
		result: brain.StreamResult{Err: errors.New("provider failed"), Recovered: true},
	}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("one"), nil
	}
	var handoffs int
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		handoffs++
		return admission("two"), true, nil
	}
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}
	if handoffs != 0 {
		t.Fatalf("handoffs = %d, want zero for recovered response", handoffs)
	}
}

func TestContinuationStopsWhenContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &continuationBrain{chunks: []string{"Answer."}}
	p := &Pipeline{Brain: b, Events: events.NewBus()}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
		return admission("one"), nil
	}
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		return admission("two"), true, nil
	}
	cancel()
	if err := p.RunTurnTextMode(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTurnTextMode() error = %v, want context canceled", err)
	}
}

func TestBackgroundTurnDoesNotInvokeHandoff(t *testing.T) {
	b := &continuationBrain{chunks: []string{"Background result."}}
	p := &Pipeline{Brain: b, Events: events.NewBus(), RouteHandoff: func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		t.Fatal("background turn invoked handoff")
		return ResponseAdmission{}, false, nil
	}}
	if err := p.runTextTurn(t.Context(), "", true); err != nil {
		t.Fatalf("runTextTurn(background) error = %v", err)
	}
}

func TestVoiceInterruptDoesNotStartHandoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	player := newFakePlayer(time.Second)
	defer player.Close()
	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"A long answer."}},
		TTS:    &fakeTTS{},
		Player: player,
		Events: events.NewBus(),
		AdmitInput: func(context.Context, string, bool) (ResponseAdmission, error) {
			return admission("one"), nil
		},
	}
	var handoffs int
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		handoffs++
		return admission("two"), true, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(ctx)
		done <- err
	}()
	select {
	case <-player.StartedSignal():
		cancel()
	case <-time.After(time.Second):
		t.Fatal("voice response did not start playback")
	}
	<-done
	if handoffs != 0 {
		t.Fatalf("handoffs = %d, want zero after voice interruption", handoffs)
	}
}
