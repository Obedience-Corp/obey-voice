package netapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// TurnRunner is the slice of pipeline.Pipeline serve drives. Text turns
// always work; voice turns require VoiceTurnRunner (STT + remote ingress).
type TurnRunner interface {
	RunTurnTextMode(ctx context.Context, input string) error
}

// VoiceTurnRunner is optional: when the serve pipeline has STT wired to a
// remote audio ingress, voice push-to-talk uses RunTurn.
type VoiceTurnRunner interface {
	TurnRunner
	RunTurn(ctx context.Context) (string, error)
}

// ErrBusy reports a full dispatch queue — the pipeline is saturated and the
// client should retry rather than silently pile up turns.
var ErrBusy = errors.New("dispatcher queue is full")

const dispatchQueueDepth = 16

type opKind int

const (
	opText opKind = iota
	opClear
	opResume
	opVoice
	opPersona
	opBackground
)

type personaResult struct {
	ack PersonaAck
	err error
}

type dispatchOp struct {
	kind        opKind
	text        string
	id          string
	done        chan error // non-nil for ops whose caller waits on the result
	setPersona  func(id string) (PersonaAck, error)
	personaDone chan personaResult
	// waitCtx is the caller's context for waitable ops (resume). If it is
	// already canceled when the op reaches apply, the work is skipped so a
	// timed-out client cannot still mutate session state later.
	waitCtx context.Context
	// prepare runs on the dispatch goroutine immediately before a voice turn
	// starts, and is where a queued voice_start takes the exclusive mic —
	// on dequeue, not on arrival. Returning false abandons the turn; the
	// caller has already told its client why.
	prepare func() bool
}

// Dispatcher serializes pipeline access: pipeline turn methods assume one
// turn owns the pipeline at a time, so every remote control message funnels
// through one loop. Interrupt is the exception — it cancels the in-flight
// turn's context out-of-band, the same per-turn-context mechanism the
// conversation TUI uses (its D1 decision).
type Dispatcher struct {
	runner       TurnRunner
	bus          *events.Bus
	clearHistory func()
	resume       func(id string) error

	queue chan dispatchOp

	mu         sync.Mutex
	cancelTurn context.CancelFunc
	active     atomic.Bool
	// canceled marks the in-flight turn as interrupted. It is separate from
	// active so /v1/status keeps reporting the truth — a canceled turn is
	// still running until it unwinds — while a voice_start arriving right
	// behind an interrupt still sees a free pipeline.
	canceled atomic.Bool
}

func NewDispatcher(runner TurnRunner, bus *events.Bus, clearHistory func(), resume func(id string) error) *Dispatcher {
	return &Dispatcher{
		runner:       runner,
		bus:          bus,
		clearHistory: clearHistory,
		resume:       resume,
		queue:        make(chan dispatchOp, dispatchQueueDepth),
	}
}

// Run processes control operations until ctx is canceled. It must run in
// exactly one goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		// User operations already queued take priority over unsolicited results.
		select {
		case op := <-d.queue:
			d.apply(ctx, op)
			continue
		default:
		}
		var ready <-chan struct{}
		if runner, ok := d.runner.(backgroundRunner); ok {
			ready = runner.BackgroundReady()
		}
		select {
		case <-ctx.Done():
			return
		case op := <-d.queue:
			d.apply(ctx, op)
		case <-ready:
			d.apply(ctx, dispatchOp{kind: opBackground})
		}
	}
}

func (d *Dispatcher) apply(ctx context.Context, op dispatchOp) {
	switch op.kind {
	case opText, opBackground:
		turnCtx, cancel := context.WithCancel(ctx)
		d.beginTurn(cancel)
		var err error
		if op.kind == opBackground {
			if runner, ok := d.runner.(backgroundRunner); ok {
				err = runner.RunBackgroundTurn(turnCtx)
			}
		} else {
			err = d.runner.RunTurnTextMode(turnCtx, op.text)
		}
		d.endTurn()
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			d.bus.Emit(events.Error{Stage: "turn", Message: err.Error()})
		}

	case opVoice:
		voice, ok := d.runner.(VoiceTurnRunner)
		if !ok {
			d.bus.Emit(events.Error{Stage: "turn", Message: "remote mic is not enabled on this serve instance"})
			return
		}
		if op.prepare != nil && !op.prepare() {
			return
		}
		turnCtx, cancel := context.WithCancel(ctx)
		d.beginTurn(cancel)
		_, err := voice.RunTurn(turnCtx)
		d.endTurn()
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			d.bus.Emit(events.Error{Stage: "turn", Message: err.Error()})
		}

	case opClear:
		if d.clearHistory != nil {
			d.clearHistory()
		}
		d.bus.Emit(events.ConversationCleared{})

	case opResume:
		if op.waitCtx != nil && op.waitCtx.Err() != nil {
			if op.done != nil {
				op.done <- op.waitCtx.Err()
			}
			return
		}
		err := errors.New("resume is not supported")
		if d.resume != nil {
			err = d.resume(op.id)
		}
		if op.done != nil {
			op.done <- err
		}

	case opPersona:
		if op.waitCtx != nil && op.waitCtx.Err() != nil {
			op.personaDone <- personaResult{err: op.waitCtx.Err()}
			return
		}
		ack, err := op.setPersona(op.id)
		op.personaDone <- personaResult{ack: ack, err: err}
	}
}

// beginTurn installs the in-flight turn's cancel and clears the interrupt flag
// as one step, under the mutex Interrupt takes. As two steps it lost an
// Interrupt that landed between them: the turn context was canceled and
// Interrupted() still reported false, so the paths that gate turn_interrupted
// on it told nobody the turn had ended.
func (d *Dispatcher) beginTurn(cancel context.CancelFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelTurn = cancel
	d.canceled.Store(false)
	d.active.Store(true)
}

// endTurn retires the turn that just unwound: nothing left to cancel, and no
// turn left to report as active.
func (d *Dispatcher) endTurn() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active.Store(false)
	d.cancelTurn = nil
}

// SubmitText enqueues one text turn; ErrBusy when the queue is full.
func (d *Dispatcher) SubmitText(text string) error {
	return d.enqueue(dispatchOp{kind: opText, text: text})
}

// SubmitVoice enqueues one remote-mic voice turn (STT → brain → TTS). The
// remote client must stream audio_input frames and voice_end on the ingress
// while this turn is active. prepare, if set, runs on the dispatch goroutine
// just before the turn and may abandon it by returning false.
func (d *Dispatcher) SubmitVoice(prepare func() bool) error {
	if _, ok := d.runner.(VoiceTurnRunner); !ok {
		return errors.New("remote mic is not enabled")
	}
	return d.enqueue(dispatchOp{kind: opVoice, prepare: prepare})
}

// VoiceEnabled reports whether the runner can execute remote-mic turns.
func (d *Dispatcher) VoiceEnabled() bool {
	_, ok := d.runner.(VoiceTurnRunner)
	return ok
}

// ClearHistory enqueues a history wipe, serialized against turns.
func (d *Dispatcher) ClearHistory() error {
	return d.enqueue(dispatchOp{kind: opClear})
}

// ResumeSession loads a session behind any in-flight turn and reports the
// result. If ctx is canceled while waiting, apply skips the resume so a
// timed-out client does not swap session state later.
func (d *Dispatcher) ResumeSession(ctx context.Context, id string) error {
	done := make(chan error, 1)
	if err := d.enqueue(dispatchOp{kind: opResume, id: id, done: done, waitCtx: ctx}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetPersona serializes a live runtime switch behind any in-flight turn and
// waits until it has actually been applied. A client cannot receive a success
// ack and enqueue its next turn ahead of the persona change.
func (d *Dispatcher) SetPersona(ctx context.Context, id string, apply func(string) (PersonaAck, error)) (PersonaAck, error) {
	if apply == nil {
		return PersonaAck{}, errors.New("set_persona is not enabled")
	}
	done := make(chan personaResult, 1)
	op := dispatchOp{kind: opPersona, id: id, setPersona: apply, personaDone: done, waitCtx: ctx}
	if err := d.enqueue(op); err != nil {
		return PersonaAck{}, err
	}
	select {
	case result := <-done:
		return result.ack, result.err
	case <-ctx.Done():
		return PersonaAck{}, ctx.Err()
	}
}

// Interrupt cancels the in-flight turn, if any. Unlike the other controls it
// does not queue — an interrupt behind the turn it targets is useless.
func (d *Dispatcher) Interrupt() {
	d.mu.Lock()
	cancel := d.cancelTurn
	if cancel != nil {
		// The turn is dead from here even though it has not unwound yet, so
		// the barge-in sequence (interrupt → voice_start) finds a free
		// pipeline instead of queueing behind the turn it just killed.
		d.canceled.Store(true)
	}
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TurnActive reports whether a turn is running right now. A canceled turn is
// still active until it unwinds — reporting otherwise would tell a client the
// pipeline is free while a slow synthesis or tool call is still burning.
func (d *Dispatcher) TurnActive() bool {
	return d.active.Load()
}

// Interrupted reports whether the in-flight turn has been canceled and is only
// unwinding.
func (d *Dispatcher) Interrupted() bool {
	return d.active.Load() && d.canceled.Load()
}

// Pending reports whether a submission would have to wait: a live turn that
// has not been interrupted, or ops already queued ahead of it. A voice_start
// that lands here is queued without claiming the mic.
func (d *Dispatcher) Pending() bool {
	return (d.active.Load() && !d.canceled.Load()) || len(d.queue) > 0
}

func (d *Dispatcher) enqueue(op dispatchOp) error {
	select {
	case d.queue <- op:
		return nil
	default:
		return ErrBusy
	}
}

type backgroundRunner interface {
	BackgroundReady() <-chan struct{}
	RunBackgroundTurn(context.Context) error
}
