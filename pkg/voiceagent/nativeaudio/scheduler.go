package nativeaudio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var ErrSchedulerUnavailable = errors.New("native audio producer did not stop; prepare the binding again")

const CaptionJoinTimeout = 5 * time.Second

// Scheduler serializes this native session's ASR/chat/speech operations.
// Optional captions yield to every foreground request and never delay it by
// waiting for an ordinary transcription completion.
type Scheduler struct {
	mu          sync.Mutex
	gate        chan struct{}
	waiting     int
	foreground  bool
	unavailable bool
	caption     *captionOperation
}
type captionOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewScheduler() *Scheduler { return &Scheduler{gate: make(chan struct{}, 1)} }

type schedulerContextKey struct{}
type schedulerToken struct {
	scheduler *Scheduler
	active    atomic.Bool
}

func (s *Scheduler) Run(ctx context.Context, operation func(context.Context) error) error {
	owned, release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return operation(owned)
}
func (s *Scheduler) acquire(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if s.unavailable {
		s.mu.Unlock()
		return nil, nil, ErrSchedulerUnavailable
	}
	s.waiting++
	caption := s.caption
	if caption != nil {
		caption.cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.waiting--; s.mu.Unlock() }()
	if caption != nil {
		if err := s.joinCaption(ctx, caption); err != nil {
			return nil, nil, err
		}
	}
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	s.mu.Lock()
	if s.unavailable {
		<-s.gate
		s.mu.Unlock()
		return nil, nil, ErrSchedulerUnavailable
	}
	s.foreground = true
	s.mu.Unlock()
	owned, cancel := context.WithCancel(ctx)
	token := &schedulerToken{scheduler: s}
	token.active.Store(true)
	owned = context.WithValue(owned, schedulerContextKey{}, token)
	var once sync.Once
	release := func() {
		once.Do(func() {
			token.active.Store(false)
			cancel()
			s.mu.Lock()
			s.foreground = false
			<-s.gate
			s.mu.Unlock()
		})
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	return owned, release, nil
}

// StartCaption returns false when foreground work owns or awaits the binding.
// The producer must honor cancellation; failure to join fences the binding.
func (s *Scheduler) StartCaption(parent context.Context, operation func(context.Context)) bool {
	if parent.Err() != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable || s.waiting > 0 || s.foreground || s.caption != nil {
		return false
	}
	select {
	case s.gate <- struct{}{}:
	default:
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	token := &schedulerToken{scheduler: s}
	token.active.Store(true)
	ctx = context.WithValue(ctx, schedulerContextKey{}, token)
	caption := &captionOperation{cancel: cancel, done: make(chan struct{})}
	s.caption = caption
	go func() {
		defer func() {
			token.active.Store(false)
			cancel()
			s.mu.Lock()
			if s.caption == caption {
				s.caption = nil
			}
			<-s.gate
			close(caption.done)
			s.mu.Unlock()
		}()
		operation(ctx)
	}()
	return true
}
func (s *Scheduler) CancelCaption(ctx context.Context) error {
	s.mu.Lock()
	caption := s.caption
	if caption != nil {
		caption.cancel()
	}
	s.mu.Unlock()
	if caption == nil {
		return nil
	}
	return s.joinCaption(ctx, caption)
}
func (s *Scheduler) joinCaption(ctx context.Context, caption *captionOperation) error {
	timer := time.NewTimer(CaptionJoinTimeout)
	defer timer.Stop()
	select {
	case <-caption.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		s.mu.Lock()
		s.unavailable = true
		s.mu.Unlock()
		return ErrSchedulerUnavailable
	}
}

func (s *Scheduler) MarkUnavailable() { s.mu.Lock(); s.unavailable = true; s.mu.Unlock() }
