package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"sync"
	"testing"
	"time"
)

func TestDroppedVoiceCommandReleasesDrainOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	m := newConversation("Test")
	m.deps = conversationDeps{ctx: ctx, wg: &wg}
	cmd := m.dispatchVoiceTurn() // Deliberately never submitted to Bubble Tea.
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dropped command stranded drain reservation")
	}
	// If the scheduler runs it late, no runtime operation or second Done occurs.
	if cmd() != nil {
		t.Fatal("abandoned command executed after shutdown")
	}
}

func TestRunningCommandRetainsDrainReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cmd := trackedCommand(ctx, &wg, func() tea.Msg { close(started); <-release; return nil })
	go cmd()
	<-started
	cancel()
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("shutdown released an executing command")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("executed command did not release reservation")
	}
}
