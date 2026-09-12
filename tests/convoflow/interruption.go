package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// runInterruptedTurn exercises the host cancellation path only after the real
// sink has reported playback start. It always joins the turn before returning,
// so the following persona selection cannot overlap its response resources.
func runInterruptedTurn(ctx context.Context, rec *recorder, delay time.Duration, run func(context.Context) error, stop func()) error {
	if delay <= 0 {
		return fmt.Errorf("interruption delay must be positive")
	}
	mark := len(rec.snapshot())
	turnCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- run(turnCtx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var ready bool
	for !ready {
		for _, te := range rec.snapshot()[mark:] {
			if started, ok := te.event.(events.SpeakingStarted); ok && started.AudibleSink == "local_speaker" {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		select {
		case err := <-done:
			joined = true
			return fmt.Errorf("turn ended before local playback began: %w", errors.Join(err, errors.New("interruption not exercised")))
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case err := <-done:
		joined = true
		return fmt.Errorf("turn ended before interruption: %w", errors.Join(err, errors.New("interruption not exercised")))
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	rec.add(events.Info{Message: "convoflow: interrupt requested after local playback started"})
	stop()
	cancel()
	err := <-done
	joined = true
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	for _, te := range rec.snapshot()[mark:] {
		if metrics, ok := te.event.(events.TurnMetrics); ok && metrics.Outcome == "interrupted" {
			return nil
		}
	}
	return fmt.Errorf("interruption produced no interrupted terminal metrics")
}
