package tui

import (
	"context"
	"fmt"
)

// captureSeal is the completion owned by an already detached capture. Keeping
// it across menu replacement prevents a second writer while I/O remains stuck.
type captureSeal interface {
	Done() <-chan struct{}
	SealError() error
	Dir() string
	Mode() string
}

// waitCaptureSeal runs after the terminal update loop (also for outer Quit and
// program errors). The caller shares its deadline with other cleanup helpers.
func (m *conversationModel) waitCaptureSeal(ctx context.Context) error {
	m.stopCapture()
	pending := m.capturePending
	if pending == nil {
		return nil
	}
	select {
	case <-pending.Done():
		if err := pending.SealError(); err != nil {
			return fmt.Errorf("capture incomplete/unsealed → %s: %w", pending.Dir(), err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("capture incomplete/unsealed → %s: %w", pending.Dir(), ctx.Err())
	}
}
