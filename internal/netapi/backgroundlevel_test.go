package netapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type levelBackgroundRunner struct {
	ready    chan struct{}
	entered  chan string
	mu       sync.Mutex
	pending  bool
	captured bool
	calls    int
}

func (r *levelBackgroundRunner) BackgroundReady() <-chan struct{} { return r.ready }
func (r *levelBackgroundRunner) BackgroundPending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending
}
func (r *levelBackgroundRunner) HasCapturedInput() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.captured
}
func (r *levelBackgroundRunner) RunTurnTextMode(context.Context, string) error {
	select {
	case r.entered <- "text":
	default:
	}
	return nil
}
func (r *levelBackgroundRunner) RunBackgroundTurn(context.Context) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case r.entered <- "background":
	default:
	}
	return nil
}
func (r *levelBackgroundRunner) setPending(v bool) {
	r.mu.Lock()
	r.pending = v
	r.mu.Unlock()
}
func (r *levelBackgroundRunner) setCaptured(v bool) {
	r.mu.Lock()
	r.captured = v
	r.mu.Unlock()
}
func (r *levelBackgroundRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestDispatcherLevelPendingDoesNotSpinWhenRunMakesNoProgress(t *testing.T) {
	r := &levelBackgroundRunner{ready: make(chan struct{}, 4), entered: make(chan string, 4)}
	r.setPending(true)
	d := NewDispatcher(r, events.NewBus(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("pending level was not reconciled")
	}
	time.Sleep(100 * time.Millisecond)
	if got := r.callCount(); got != 1 {
		t.Fatalf("unchanged pending level dispatched %d times before recovery", got)
	}
	cancel()
	<-done
}

func TestDispatcherReconcilesPendingAfterQueuedUserTurn(t *testing.T) {
	r := &levelBackgroundRunner{ready: make(chan struct{}, 1), entered: make(chan string, 4)}
	r.setPending(true)
	d := NewDispatcher(r, events.NewBus(), nil, nil)
	if err := d.SubmitText("user"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	for _, want := range []string{"text", "background"} {
		select {
		case got := <-r.entered:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("did not run %s", want)
		}
	}
	cancel()
	<-done
}

func TestDispatcherDefersPendingWhileCapturedInputIsRetained(t *testing.T) {
	r := &levelBackgroundRunner{ready: make(chan struct{}, 1), entered: make(chan string, 2)}
	r.setPending(true)
	r.setCaptured(true)
	d := NewDispatcher(r, events.NewBus(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if got := r.callCount(); got != 0 {
		t.Fatalf("dispatched with retained captured input: %d", got)
	}
	r.setCaptured(false)
	select {
	case got := <-r.entered:
		if got != "background" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("recovery did not reconcile pending level")
	}
	cancel()
	<-done
}
