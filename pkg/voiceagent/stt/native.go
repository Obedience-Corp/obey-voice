package stt

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// NativeCapture uses the existing VAD, endpoint and completion owner without
// constructing any recognizer. The caller owns source/VAD and the media session.
type NativeCapture struct {
	cfg    config.Config
	source audio.FrameSource
	vad    *audio.VAD
	media  *chatmedia.AudioSession
	finite bool
	mu     sync.Mutex
	active bool
}

func NewNativeCapture(cfg *config.Config, source audio.FrameSource, vad *audio.VAD, media *chatmedia.AudioSession, finite bool) (*NativeCapture, error) {
	if cfg == nil || source == nil || vad == nil || media == nil {
		return nil, errors.New("native capture requires config, source, VAD and media owner")
	}
	return &NativeCapture{cfg: *cfg, source: source, vad: vad, media: media, finite: finite}, nil
}
func (p *NativeCapture) Available() bool {
	return p != nil && p.source != nil && p.vad != nil && p.media != nil
}

var nativeCaptureEpoch atomic.Uint64

func (p *NativeCapture) Start(ctx context.Context) (utterance.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("create utterance identity: %w", err)
	}
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return nil, errors.New("native capture already owns a reader")
	}
	p.active = true
	epoch := nativeCaptureEpoch.Add(1)
	p.mu.Unlock()
	sessionCtx, cancel := context.WithCancel(ctx)
	base := &sherpaSession{cancel: cancel, events: make(chan Event, 8), done: make(chan struct{}), pause: newPauseController("native"), playback: true}
	session := &nativeSession{sherpaSession: base, events: make(chan utterance.Event, 8), bridgeDone: make(chan struct{}), media: p.media, completions: make(chan YieldedForCompletion, 1)}
	policy := endpoint.FromConfig(&p.cfg, p.finite)
	if policy.MaxUtterance <= 0 || policy.MaxUtterance > audio.SamplesDuration(utterance.MaxSamples) {
		policy.MaxUtterance = audio.SamplesDuration(utterance.MaxSamples)
	}
	finalizer := &nativeFinalizer{maxSamples: int(policy.MaxUtterance * time.Duration(utterance.SampleRate) / time.Second), accept: func(ctx context.Context, samples []float32, spans []utterance.Span, started, ended time.Time, reason string) error {
		ref, err := p.media.ImportSamples(ctx, samples)
		if err != nil {
			return err
		}
		input := &utterance.Finalized{ID: fmt.Sprintf("capture-%d-%x", epoch, nonce), CaptureEpoch: epoch, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}, StartedAt: started, EndedAt: ended, EndpointReason: reason, Spans: spans}
		if err := input.Validate(); err != nil {
			p.media.Release(ref)
			return err
		}
		select {
		case base.events <- nativeFinalized{input: input, release: func() { p.media.Release(ref) }}:
			return nil
		case <-ctx.Done():
			p.media.Release(ref)
			return ctx.Err()
		}
	}}
	deps := offlineLoopDeps{frames: p.source, seg: newVADSegmenter(p.vad, preRollSamplesFromMS(p.cfg.VADPreRollMS)), policy: policy, pause: base.pause, native: finalizer}
	go func() {
		defer close(base.done)
		defer close(base.events)
		defer func() { p.mu.Lock(); p.active = false; p.mu.Unlock() }()
		runOfflineLoop(sessionCtx, deps, base.events)
	}()
	go session.bridge(sessionCtx)
	return session, nil
}

type nativeFinalized struct {
	input   *utterance.Finalized
	release func()
}

func (nativeFinalized) sttEvent() string { return "native_finalized" }

type nativeSession struct {
	*sherpaSession
	events      chan utterance.Event
	bridgeDone  chan struct{}
	media       *chatmedia.AudioSession
	completions chan YieldedForCompletion
}

func (s *nativeSession) CompletionEvents() <-chan YieldedForCompletion { return s.completions }
func (s *nativeSession) Events() <-chan utterance.Event                { return s.events }
func (s *nativeSession) Close() error {
	err := s.sherpaSession.Close()
	<-s.bridgeDone
	for event := range s.events {
		if event.Input != nil && event.Input.Audio != nil {
			s.media.Release(*event.Input.Audio)
		}
	}
	return err
}
func (s *nativeSession) bridge(ctx context.Context) {
	defer close(s.bridgeDone)
	defer close(s.events)
	if s.completions != nil {
		defer close(s.completions)
	}
	for event := range s.sherpaSession.events {
		var next utterance.Event
		var release func()
		switch e := event.(type) {
		case YieldedForCompletion:
			select {
			case s.completions <- e:
			case <-ctx.Done():
			}
			continue
		case nativeFinalized:
			next = utterance.Event{Kind: utterance.FinalizedEvent, Input: e.input}
			release = e.release
		case PhaseEvent:
			next = utterance.Event{Kind: utterance.Phase, Phase: e.Phase, Elapsed: time.Duration(e.Elapsed)}
		case InputLevel:
			next = utterance.Event{Kind: utterance.Level, Level: e.Level}
		case Timeout:
			next = utterance.Event{Kind: utterance.Timeout}
		case Failure:
			next = utterance.Event{Kind: utterance.Failure, Err: e.Err}
		default:
			continue
		}
		if ctx.Err() != nil {
			if release != nil {
				release()
			}
			continue
		}
		select {
		case s.events <- next:
		case <-ctx.Done():
			if release != nil {
				release()
			}
		}
	}
}
