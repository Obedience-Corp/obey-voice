package netapi

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// interruptRaceRunner checks the invariant every turn-state report rests on: a
// turn that starts on a dead context must agree it was interrupted. When it
// does not, the pipeline runs a turn nobody will ever be told ended —
// voiceTurnStalled and the remote-interrupt path both gate turn_interrupted on
// Interrupted().
type interruptRaceRunner struct {
	d          *Dispatcher
	turns      atomic.Int64
	violations atomic.Int64
}

func (r *interruptRaceRunner) RunTurnTextMode(ctx context.Context, _ string) error {
	return r.check(ctx)
}

func (r *interruptRaceRunner) RunTurn(ctx context.Context) (string, error) {
	return "", r.check(ctx)
}

// check gives an interrupt that is already past the dispatcher's mutex a moment
// to cancel the context, so the window is exercised instead of mostly missed.
func (r *interruptRaceRunner) check(ctx context.Context) error {
	r.turns.Add(1)
	for deadline := time.Now().Add(50 * time.Microsecond); ctx.Err() == nil && time.Now().Before(deadline); {
	}
	if ctx.Err() != nil && !r.d.Interrupted() {
		r.violations.Add(1)
	}
	return ctx.Err()
}

// TestInterruptAtTurnStartIsNeverLost hammers Interrupt against turn starts.
// Installing the cancel and clearing the interrupt flag as two steps loses an
// interrupt that lands between them: the turn context is canceled and
// Interrupted() still reports false.
func TestInterruptAtTurnStartIsNeverLost(t *testing.T) {
	const turns = 4000

	tests := []struct {
		name   string
		submit func(d *Dispatcher) error
	}{
		{"text turn", func(d *Dispatcher) error { return d.SubmitText("go") }},
		{"voice turn", func(d *Dispatcher) error { return d.SubmitVoice(nil) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &interruptRaceRunner{}
			d := NewDispatcher(runner, events.NewBus(), nil, nil)
			runner.d = d

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var wg sync.WaitGroup
			wg.Go(func() { d.Run(ctx) })

			stop := make(chan struct{})
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
						d.Interrupt()
					}
				}
			})

			for range turns {
				for tt.submit(d) != nil {
					time.Sleep(time.Millisecond)
				}
			}
			eventually(t, "every submitted turn to run", func() bool {
				return runner.turns.Load() == turns
			})

			close(stop)
			cancel()
			wg.Wait()

			if got := runner.violations.Load(); got != 0 {
				t.Fatalf("%d of %d turns started on a canceled context while Interrupted() reported false", got, turns)
			}
		})
	}
}
