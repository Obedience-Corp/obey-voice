package main

import (
	"context"
	"errors"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// hardwareReadiness waits for the production STT lifecycle to enter listening
// before a physical fixture is played. It avoids guessed startup sleeps and is
// intentionally independent of recorder timing.
type hardwareReadiness struct {
	ready  chan struct{}
	remove func()
	once   sync.Once
}

func newHardwareReadiness(bus *events.Bus) (*hardwareReadiness, error) {
	if bus == nil {
		return nil, errors.New("hardware readiness: nil event bus")
	}
	r := &hardwareReadiness{ready: make(chan struct{})}
	r.remove = bus.SubscribeAll(func(event events.Event) {
		phase, ok := event.(events.STTPhase)
		if ok && phase.Phase == "listening" {
			r.once.Do(func() { close(r.ready) })
		}
	})
	return r, nil
}

func (r *hardwareReadiness) wait(ctx context.Context) error {
	if r == nil {
		return errors.New("hardware readiness: nil barrier")
	}
	if ctx == nil {
		return errors.New("hardware readiness: nil context")
	}
	select {
	case <-r.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *hardwareReadiness) close() {
	if r != nil && r.remove != nil {
		r.remove()
		r.remove = nil
	}
}
