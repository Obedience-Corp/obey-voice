package stt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type playbackCommandKind uint8

const (
	playbackBind playbackCommandKind = iota
	playbackConfirm
	playbackFinish
)

type playbackCommand struct {
	kind    playbackCommandKind
	ctx     context.Context
	token   PauseToken
	attempt audio.PlaybackAttempt
	preRoll time.Duration
	retain  bool
	result  chan error
	state   atomic.Uint32 // 0 pending, 1 applying, 2 acknowledged, 3 canceled before application
}

func (p *pauseController) playbackCommand(ctx context.Context, command *playbackCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command.ctx, command.result = ctx, make(chan error, 1)
	select {
	case p.playbackCommands <- command:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return p.stoppedPlaybackCommand(command)
	}
	select {
	case err := <-command.result:
		return err
	case <-ctx.Done():
		if command.state.CompareAndSwap(0, 3) {
			return ctx.Err()
		}
		// Once the owner starts an operation it cannot be rolled back (for
		// example source retention). Return its acknowledgement, never an
		// ambiguous timeout that could orphan a successfully bound attempt.
		return <-command.result
	case <-p.done:
		if command.state.CompareAndSwap(0, 3) {
			return p.stoppedPlaybackCommand(command)
		}
		return <-command.result
	}
}

func (r *completionFrameReader) apply(command *playbackCommand, reset func() error) {
	if !command.state.CompareAndSwap(0, 1) {
		return
	}
	err := r.command(command, reset)
	command.result <- err
	command.state.Store(2)
}

// Accepted input may finalize before the pipeline has joined its response
// workers. The queued terminal event still belongs to the retained session;
// matching cleanup must not prevent ResumeCapture from transferring it.
func (p *pauseController) stoppedPlaybackCommand(command *playbackCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if command.kind == playbackFinish && p.stopped && p.token == command.token && p.playbackAttemptID != "" && p.playbackAttemptID == command.attempt.ID {
		return nil
	}
	return errors.New("stt: session closed")
}

type completionRetainer interface {
	RetainForNextListen(audio.Frame, time.Duration) (audio.Frame, error)
}

// completionFrameReader belongs to one STT loop. It holds at most the one
// already-read frame; the existing capture ring owns the rest of playback PCM.
// No detector or recognizer consumes parked audio before ordinary barge policy
// accepts it. Unknown and boundary-straddling frames remain ordinary input.
type completionFrameReader struct {
	owner                  *pauseController
	frames                 audio.FrameSource
	binding                *playbackCommand
	parked                 *audio.Frame
	accepted               bool
	checking               bool
	nextOffset, generation uint64
	pendingGap             audio.CaptureGap
	failure                error
}

func (r *completionFrameReader) command(command *playbackCommand, reset func() error) error {
	if err := command.ctx.Err(); err != nil {
		return err
	}
	p := r.owner
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || command.token != p.token {
		return errors.New("stt: stale completion playback token")
	}
	if command.kind == playbackBind {
		if r.binding != nil || !p.paused || p.captured || p.active {
			return errors.New("stt: capture is not idle for completion playback")
		}
		if command.attempt.ID == "" || command.attempt.Render == nil || command.attempt.CaptureEpoch != command.token.Permit.CaptureEpoch {
			return errors.New("stt: invalid completion playback attempt")
		}
		if _, ok := r.frames.(completionRetainer); !ok {
			return ErrCompletionPlaybackUnsupported
		}
		if capability, ok := r.frames.(interface{ CanRetainForNextListen() bool }); ok && !capability.CanRetainForNextListen() {
			return ErrCompletionPlaybackUnsupported
		}
		command.preRoll = max(0, min(command.preRoll, 2*time.Second))
		r.binding = command
		r.accepted = false
		p.playbackBound = true
		p.playbackAttemptID = command.attempt.ID
		return nil
	}
	if r.binding == nil || r.binding.token != command.token || r.binding.attempt.ID != command.attempt.ID {
		return errors.New("stt: stale completion playback attempt")
	}
	if command.kind == playbackConfirm || command.retain {
		r.accepted = true
		p.captured = true
		// The pipeline revokes the completion lease after this acknowledgement.
		// Releasing the park must not await another capture callback.
		if p.paused {
			// Generation/playback time is not part of the resumed listen
			// budget. Both loops consume this before endpoint evaluation.
			p.resumed = true
		}
		p.paused = false
		r.checking = true
	}
	if command.kind == playbackFinish {
		_, rendered := r.binding.attempt.Render.FirstRender()
		if !r.accepted && !p.captured && rendered {
			// Finish may arrive before the next source poll parks a frame.
			// The unread ring still contains playback PCM in that case; an
			// empty parked frame asks the source to retain only its ring tail.
			var parked audio.Frame
			if r.parked != nil {
				parked = *r.parked
			}
			// The owner loop serializes these operations. Do not hold the
			// shared pause mutex across source work or native recognizer reset.
			p.mu.Unlock()
			frame, err := r.frames.(completionRetainer).RetainForNextListen(parked, r.binding.preRoll)
			if err == nil {
				err = reset()
			}
			p.mu.Lock()
			if err != nil {
				r.failure = fmt.Errorf("stt: restore completion listen tail: %w", err)
				return r.failure
			}
			if p.stopped || p.token != command.token {
				return errors.New("stt: completion capture closed during retention")
			}
			r.parked = &frame
			r.pendingGap = frame.CaptureGap
			r.parked.CaptureGap = audio.CaptureGap{}
			r.checking = false
			p.active = false
		}
		r.binding = nil
		p.playbackBound = false
	}
	return nil
}

func (r *completionFrameReader) read(ctx context.Context, admitted bool, reset func() error) frameRead {
	if r.owner == nil {
		return readLoopFrame(ctx, r.frames)
	}
	for {
		if r.failure != nil {
			return frameRead{err: r.failure}
		}
		select {
		case command := <-r.owner.playbackCommands:
			r.apply(command, reset)
			continue
		default:
		}
		if r.parked != nil && r.binding != nil && !r.accepted {
			select {
			case command := <-r.owner.playbackCommands:
				r.apply(command, reset)
			case <-ctx.Done():
				return frameRead{err: ctx.Err()}
			case <-r.owner.done:
				return frameRead{err: errors.New("stt: session closed")}
			}
			continue
		}
		var read frameRead
		if r.parked != nil {
			frame := *r.parked
			r.parked = nil
			if len(frame.Samples) == 0 && !frame.Final {
				continue
			}
			read = frameRead{frame: frame, ready: true, eof: frame.Final}
		} else {
			read = readLoopFrame(ctx, r.frames)
		}
		if read.err != nil || !read.ready || len(read.frame.Samples) == 0 {
			return read
		}
		mergeCaptureGap(&read.frame.CaptureGap, r.pendingGap)
		r.pendingGap = audio.CaptureGap{}
		if r.checking {
			start, end, generation, known := captureInterval(read.frame)
			if !known || captureLost(read.frame) || (r.generation != 0 && (generation != r.generation || start != r.nextOffset)) {
				return frameRead{err: errors.New("stt: accepted completion capture has missing or discontinuous source coverage")}
			}
			r.nextOffset, r.generation = end, generation
		}
		if r.binding != nil && !r.accepted && !admitted && whollyAfterRender(read.frame, r.binding.attempt.Render) {
			frame := read.frame
			frame.Samples = slices.Clone(frame.Samples)
			frame.CaptureSpans = slices.Clone(frame.CaptureSpans)
			r.parked = &frame
			r.nextOffset, r.generation = 0, 0
			continue
		}
		if _, end, generation, known := captureInterval(read.frame); known {
			r.nextOffset, r.generation = end, generation
		} else {
			r.nextOffset, r.generation = 0, 0
		}
		return read
	}
}

func captureInterval(frame audio.Frame) (start, end, generation uint64, known bool) {
	remaining := len(frame.Samples)
	if remaining == 0 || len(frame.CaptureSpans) == 0 {
		return
	}
	for i, span := range frame.CaptureSpans {
		if span.SampleCount <= 0 || span.SampleCount > remaining || span.DeviceGeneration == 0 || span.CallbackAt.IsZero() || span.EstimatedStart.IsZero() {
			return 0, 0, 0, false
		}
		if i == 0 {
			start, end, generation = span.SampleOffset, span.SampleOffset, span.DeviceGeneration
		}
		if span.SampleOffset != end || span.DeviceGeneration != generation {
			return 0, 0, 0, false
		}
		end += uint64(span.SampleCount)
		remaining -= span.SampleCount
	}
	return start, end, generation, remaining == 0
}

func whollyAfterRender(frame audio.Frame, boundary *audio.FirstRenderBoundary) bool {
	at, rendered := boundary.FirstRender()
	if !rendered {
		return false
	}
	if _, _, _, known := captureInterval(frame); !known || captureLost(frame) {
		return false
	}
	for _, span := range frame.CaptureSpans {
		if span.EstimatedStart.Before(at) {
			return false
		}
	}
	return true
}

func captureLost(frame audio.Frame) bool {
	g := frame.CaptureGap
	if g.OverflowSamples != 0 || g.DiscardedSamples != 0 || g.Reset || g.DeviceDiscontinuity || g.SubscriptionDroppedSamples != 0 || g.SubscriptionDroppedCallbacks != 0 || g.SubscriptionDeviceDiscontinuity {
		return true
	}
	for _, span := range frame.CaptureSpans {
		if span.DeviceDiscontinuity {
			return true
		}
	}
	return false
}

func mergeCaptureGap(to *audio.CaptureGap, from audio.CaptureGap) {
	to.OverflowSamples += from.OverflowSamples
	to.DiscardedSamples += from.DiscardedSamples
	to.Reset = to.Reset || from.Reset
	to.DeviceDiscontinuity = to.DeviceDiscontinuity || from.DeviceDiscontinuity
	to.SubscriptionDroppedSamples += from.SubscriptionDroppedSamples
	to.SubscriptionDroppedCallbacks += from.SubscriptionDroppedCallbacks
	to.SubscriptionDeviceDiscontinuity = to.SubscriptionDeviceDiscontinuity || from.SubscriptionDeviceDiscontinuity
}
