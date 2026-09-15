package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// runRetainedTurn observes only the UserInput events emitted by one retained
// turn. A mismatch cancels that turn's child context so a known-wrong acoustic
// transcript does not wait for an expensive provider response. The observed
// text is returned for the caller's normal final verification and reporting.
// The observer is detached before this function returns and never waits from
// inside the event-bus callback.
func runRetainedTurn(ctx context.Context, bus *events.Bus, expected string, run func(context.Context) (string, error)) (text string, err error, mismatched bool) {
	if ctx == nil {
		return "", errors.New("retained turn: nil context"), false
	}
	if run == nil {
		return "", errors.New("retained turn: nil runner"), false
	}
	if bus == nil {
		text, err := run(ctx)
		return text, err, false
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	wrong := make(chan string, 1)
	remove := bus.ObserveBeforeFanout(func(event events.Event) {
		input, ok := event.(events.UserInput)
		if !ok || normalizeAcousticText(input.Text) == normalizeAcousticText(expected) {
			return
		}
		select {
		case wrong <- input.Text:
		default:
		}
	})
	defer remove()

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := run(child)
		done <- result{text: text, err: err}
	}()

	select {
	case observed := <-wrong:
		cancel()
		result := <-done
		// The mismatch is the useful failure. Suppress the expected child
		// cancellation so the caller can run its existing transcript check.
		if result.err != nil && !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
			return observed, errors.Join(fmt.Errorf("retained turn canceled after transcript mismatch"), result.err), true
		}
		return observed, nil, true
	case result := <-done:
		// A synchronous provider/test double can emit UserInput and complete
		// before the select observes either channel. Prefer the observed
		// mismatch so the caller does not wait or report a false success.
		select {
		case observed := <-wrong:
			if result.err != nil && !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
				return observed, errors.Join(fmt.Errorf("retained turn canceled after transcript mismatch"), result.err), true
			}
			return observed, nil, true
		default:
		}
		return result.text, result.err, false
	case <-ctx.Done():
		cancel()
		result := <-done
		return result.text, errors.Join(ctx.Err(), result.err), false
	}
}
