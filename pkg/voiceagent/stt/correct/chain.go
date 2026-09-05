// Package correct rewrites final speech-to-text transcripts with pure,
// deterministic stages. It knows nothing about audio, providers, or config.
package correct

import (
	"context"
	"fmt"
)

// Stage rewrites a transcript. Implementations must be pure: no I/O and no
// shared mutable state, so a stage can run on every final transcript.
type Stage interface {
	Name() string
	Apply(ctx context.Context, text string) (string, error)
}

// Chain applies stages in order.
type Chain struct {
	stages []Stage
}

// New builds a chain from stages; nil stages are skipped.
func New(stages ...Stage) Chain {
	c := Chain{}
	for _, s := range stages {
		if s != nil {
			c.stages = append(c.stages, s)
		}
	}
	return c
}

// Len reports how many stages the chain runs.
func (c Chain) Len() int { return len(c.stages) }

// Correct runs every stage. When a stage fails, the text produced by the
// stages before it is returned together with the error so callers can fail
// open. Cancellation is checked before each stage.
func (c Chain) Correct(ctx context.Context, text string) (string, error) {
	for _, s := range c.stages {
		if err := ctx.Err(); err != nil {
			return text, err
		}
		out, err := s.Apply(ctx, text)
		if err != nil {
			return text, fmt.Errorf("correct: stage %s: %w", s.Name(), err)
		}
		text = out
	}
	return text, nil
}
