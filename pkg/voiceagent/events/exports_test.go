package events

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEventTypeExposesWireNames(t *testing.T) {
	tests := []struct {
		event Event
		want  string
	}{
		{UserInput{}, "user_input"},
		{ResponseReady{}, "response_ready"},
		{TurnMetrics{}, "turn_metrics"},
		{SpeechDiagnostic{}, "speech_diagnostic"},
		{TokenUsage{}, "token_usage"},
		{SessionWarning{}, "session_warning"},
		{ConversationCompacted{}, "conversation_compacted"},
		{ContinuityBudget{}, "continuity.budget"},
		{ContinuityTruncated{}, "continuity.truncated"},
		{ContinuityRecovery{}, "continuity.recovery"},
		{TurnInterrupted{}, "turn_interrupted"},
		{ConversationCleared{}, "conversation_cleared"},
		{ThinkingDelta{}, "thinking_delta"},
		{ResponseSpeakableStarted{}, "response_speakable_started"},
		{JobStarted{}, "job_started"},
		{JobFinished{}, "job_finished"},
		{JobFailed{}, "job_failed"},
	}
	for _, tt := range tests {
		if got := EventType(tt.event); got != tt.want {
			t.Errorf("EventType(%T) = %q, want %q", tt.event, got, tt.want)
		}
	}
}

func TestSubscribeAllReceivesEveryType(t *testing.T) {
	bus := NewBus()
	var got []string
	bus.SubscribeAll(func(e Event) { got = append(got, EventType(e)) })

	bus.Emit(UserInput{Text: "hi"})
	bus.Emit(ThinkingStarted{})
	bus.Emit(ResponseReady{Response: "hello"})

	want := []string{"user_input", "thinking_started", "response_ready"}
	if len(got) != len(want) {
		t.Fatalf("received %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSubscribeAllRemoveDetaches(t *testing.T) {
	bus := NewBus()
	count := 0
	remove := bus.SubscribeAll(func(Event) { count++ })

	bus.Emit(Info{Message: "one"})
	remove()
	bus.Emit(Info{Message: "two"})
	remove() // idempotent

	if count != 1 {
		t.Fatalf("handler ran %d times after removal, want 1", count)
	}
}

func TestSubscribeDetachableRemovesHandler(t *testing.T) {
	bus := NewBus()
	count := 0
	remove := SubscribeDetachable(bus, func(Info) { count++ })

	bus.Emit(Info{Message: "one"})
	remove()
	bus.Emit(Info{Message: "two"})
	remove()

	if count != 1 {
		t.Fatalf("typed handler ran %d times after removal, want 1", count)
	}
}

func TestSubscribeAllDoesNotDisturbTypedSubscribers(t *testing.T) {
	bus := NewBus()
	var order []string
	Subscribe(bus, func(Info) { order = append(order, "typed") })
	bus.SubscribeAll(func(Event) { order = append(order, "all") })

	bus.Emit(Info{Message: "x"})

	if len(order) != 2 || order[0] != "typed" || order[1] != "all" {
		t.Fatalf("dispatch order = %v, want [typed all]", order)
	}
}

func TestSubscribeAllConcurrentAttachDetach(t *testing.T) {
	bus := NewBus()
	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				remove := bus.SubscribeAll(func(Event) {})
				bus.Emit(Info{Message: fmt.Sprintf("g%d-%d", i, j)})
				remove()
			}
		})
	}
	wg.Wait()

	bus.mu.RLock()
	leaked := len(bus.allHandlers)
	bus.mu.RUnlock()
	if leaked != 0 {
		t.Fatalf("%d handlers leaked after all removals", leaked)
	}
}

func TestSpeakingWindowUsesPlayElapsedNotMixedOrigins(t *testing.T) {
	m := TurnMetrics{
		PlaybackStartElapsed:    10 * time.Second,
		PlaybackCompleteElapsed: 2 * time.Second, // T_eou; must not be subtracted
		DeadAirElapsed:          time.Second,
		Segments:                []SegmentMetric{{PlayElapsed: 5 * time.Second}},
	}
	if got, want := m.SpeakingWindow(), 6*time.Second; got != want {
		t.Fatalf("SpeakingWindow = %v, want %v", got, want)
	}
	if (TurnMetrics{}).SpeakingWindow() != 0 {
		t.Fatal("empty metrics must not invent a speaking window")
	}
	m.AudibleSink = "local_speaker"
	if got, want := m.SpeakingWindow(), 6*time.Second; got != want {
		t.Fatalf("local_speaker SpeakingWindow = %v, want %v", got, want)
	}
	for _, sink := range []string{"stream_client", "none"} {
		m.AudibleSink = sink
		if got := m.SpeakingWindow(); got != 0 {
			t.Fatalf("AudibleSink %q SpeakingWindow = %v, want 0 (drain is not speaking)", sink, got)
		}
	}
}
