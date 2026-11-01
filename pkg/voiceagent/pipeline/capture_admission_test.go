package pipeline

import (
	"context"
	"errors"
	"testing"
)

func TestCapturePreparationFailureStopsBeforeOpeningReader(t *testing.T) {
	expected := errors.New("current context cannot be prepared")
	p := &Pipeline{BeforeCapture: func(ctx context.Context) error { return expected }}
	if _, err := p.RunTurn(t.Context()); !errors.Is(err, expected) {
		t.Fatal("capture admission error was lost", err)
	}
	// With no brain or capture installed, reaching ordinary turn execution would
	// panic or produce a different error. Preparation failed before either owner.
}
