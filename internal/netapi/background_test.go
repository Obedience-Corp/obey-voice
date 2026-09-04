package netapi

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"sync"
	"testing"
	"time"
)

type backgroundTestRunner struct {
	ready   chan struct{}
	entered chan string
	release chan struct{}
	mu      sync.Mutex
	active  int
	overlap bool
}

func (r *backgroundTestRunner) BackgroundReady() <-chan struct{} { return r.ready }
func (r *backgroundTestRunner) enter(ctx context.Context, label string) error {
	r.mu.Lock()
	r.active++
	if r.active > 1 {
		r.overlap = true
	}
	r.mu.Unlock()
	r.entered <- label
	if label == "first" {
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return ctx.Err()
}
func (r *backgroundTestRunner) RunTurnTextMode(ctx context.Context, text string) error {
	return r.enter(ctx, text)
}
func (r *backgroundTestRunner) RunBackgroundTurn(ctx context.Context) error {
	return r.enter(ctx, "background")
}
func TestDispatcherDeliversBackgroundAfterQueuedUserTurns(t *testing.T) {
	r := &backgroundTestRunner{ready: make(chan struct{}, 1), entered: make(chan string, 3), release: make(chan struct{})}
	d := NewDispatcher(r, events.NewBus(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	if err := d.SubmitText("first"); err != nil {
		t.Fatal(err)
	}
	if got := <-r.entered; got != "first" {
		t.Fatal(got)
	}
	if err := d.SubmitText("second"); err != nil {
		t.Fatal(err)
	}
	r.ready <- struct{}{}
	select {
	case got := <-r.entered:
		t.Fatalf("interrupted active user turn with %s", got)
	default:
	}
	close(r.release)
	for _, want := range []string{"second", "background"} {
		select {
		case got := <-r.entered:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("queued result not delivered")
		}
	}
	cancel()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overlap {
		t.Fatal("concurrent pipeline ownership")
	}
}
