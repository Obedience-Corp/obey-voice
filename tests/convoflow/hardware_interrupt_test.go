package main

import (
	"context"
	"strings"
	"testing"
)

func TestAcousticInterruptionRejectsBeforeOpeningDevices(t *testing.T) {
	if err := (*liveRunner)(nil).runAcousticInterruption(context.Background(), "initial.wav", "interrupt.wav", "answer", t.TempDir()); err == nil || !strings.Contains(err.Error(), "agent is not configured") {
		t.Fatalf("nil runner error = %v", err)
	}
}
