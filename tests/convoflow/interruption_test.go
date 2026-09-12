package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestInterruptionJoinsBeforeNextControlAndNeedsLocalPlayback(t *testing.T) {
	for _, sink := range []string{"local_speaker", "none", "stream_client"} {
		t.Run(sink, func(t *testing.T) {
			rec := &recorder{}
			stopped := false
			joined := make(chan struct{})
			err := runInterruptedTurn(context.Background(), rec, time.Millisecond, func(ctx context.Context) error {
				defer close(joined)
				rec.add(events.SpeakingStarted{AudibleSink: sink})
				if sink != "local_speaker" {
					return nil
				}
				<-ctx.Done()
				rec.add(events.TurnMetrics{Outcome: "interrupted"})
				return ctx.Err()
			}, func() { stopped = true })
			select {
			case <-joined:
			default:
				t.Fatal("returned before turn joined")
			}
			if sink == "local_speaker" {
				if err != nil || !stopped {
					t.Fatalf("interruption = %v, stopped %v", err, stopped)
				}
			} else if err == nil || stopped {
				t.Fatalf("nonlocal sink counted as interruption: %v", err)
			}
		})
	}
}

func TestInterruptionRefusesAlreadyCompletedSpeech(t *testing.T) {
	rec := &recorder{}
	err := runInterruptedTurn(context.Background(), rec, time.Second, func(context.Context) error {
		rec.add(events.SpeakingStarted{AudibleSink: "local_speaker"})
		rec.add(events.TurnMetrics{Outcome: "completed"})
		return nil
	}, func() { t.Error("stopped completed turn") })
	if err == nil || !strings.Contains(err.Error(), "interruption not exercised") {
		t.Fatalf("result = %v", err)
	}
}

func TestInterruptScenarioRequiresUserTurn(t *testing.T) {
	for _, turn := range []string{
		"id: invalid\n    user: hello\n    interrupt_after: -1ms",
		"id: invalid\n    op: select_persona\n    persona: santa\n    interrupt_after: 1ms",
		"id: invalid\n    interrupt_after: 1ms",
	} {
		if _, err := loadScenarioString("invalid", "name: interruption\nturns:\n  - "+turn+"\n"); err == nil {
			t.Fatalf("accepted invalid interruption: %s", turn)
		}
	}
}
