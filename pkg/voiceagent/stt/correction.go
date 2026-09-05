package stt

import (
	"context"
	"sync"
)

// Corrector rewrites a final transcript. Implementations are pure.
type Corrector interface {
	Correct(ctx context.Context, text string) (string, error)
}

// WithCorrection returns a Provider whose sessions pass every FinalTranscript
// through c before it is emitted. Partial transcripts and every other event
// are forwarded untouched. When c fails, the raw transcript is forwarded and
// the failure is reported to logf when one is given. A nil provider or
// corrector returns p unchanged.
func WithCorrection(p Provider, c Corrector, logf func(string, ...any)) Provider {
	if p == nil || c == nil {
		return p
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &correctedProvider{Provider: p, corrector: c, logf: logf}
}

type correctedProvider struct {
	Provider
	corrector Corrector
	logf      func(string, ...any)
}

// Start implements Provider. The corrector runs with the context given here.
func (p *correctedProvider) Start(ctx context.Context) (Session, error) {
	inner, err := p.Provider.Start(ctx)
	if err != nil {
		return nil, err
	}
	s := &correctedSession{inner: inner, events: make(chan Event, 8), done: make(chan struct{})}
	go s.run(ctx, p.corrector, p.logf)
	return s, nil
}

// correctedSession forwards an inner session's events, correcting finals. It
// stops when the inner channel closes, which every provider in this package
// does after Close, or when its own Close is called while a consumer has
// stopped reading.
type correctedSession struct {
	inner  Session
	events chan Event
	done   chan struct{}
	once   sync.Once
}

func (s *correctedSession) Events() <-chan Event { return s.events }

func (s *correctedSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return s.inner.Close()
}

func (s *correctedSession) run(ctx context.Context, c Corrector, logf func(string, ...any)) {
	defer close(s.events)
	for ev := range s.inner.Events() {
		if fin, ok := ev.(FinalTranscript); ok && fin.Text != "" {
			out, err := c.Correct(ctx, fin.Text)
			if err != nil {
				logf("stt correction skipped: %v", err)
			} else {
				fin.Text = out
			}
			ev = fin
		}
		select {
		case s.events <- ev:
		case <-s.done:
			return
		}
	}
}
