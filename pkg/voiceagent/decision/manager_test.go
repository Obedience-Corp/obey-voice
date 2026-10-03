package decision

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type countCloser struct{ closed atomic.Int32 }

func (c *countCloser) Close() error { c.closed.Add(1); return nil }

func waitPhase(t *testing.T, m *Manager, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.Status().Phase == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase=%+v want %s", m.Status(), want)
}

func TestManagerFencesSlowSetupAndClosesItsResources(t *testing.T) {
	var controller Controller
	m := NewManager(&controller)
	defer m.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	oldCloser := &countCloser{}
	options := Options{Budget: time.Second, VoiceWork: true}
	m.Configure(options, func(ctx context.Context) (Evaluator, io.Closer, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return evaluatorFunc(func(context.Context, Request) (Response, error) {
			t.Error("stale provider used")
			return Response{}, nil
		}), oldCloser, nil
	})
	<-entered
	m.Configure(Options{}, nil)
	if controller.Enabled(VoiceWork) || m.Status().Phase != "off" {
		t.Fatal("disable waited for old setup")
	}
	close(release)
	current := evaluatorFunc(func(context.Context, Request) (Response, error) { return certain(Simple), nil })
	m.Configure(options, func(context.Context) (Evaluator, io.Closer, error) { return current, nil, nil })
	waitPhase(t, m, "ready")
	if !controller.Enabled(VoiceWork) {
		t.Fatal("new provider not enabled")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if oldCloser.closed.Load() != 1 {
		t.Fatalf("stale resource closed %d times", oldCloser.closed.Load())
	}
	if controller.Enabled(VoiceWork) {
		t.Fatal("closed runtime left decisions enabled")
	}
}

func TestManagerOffDoesNotPrepare(t *testing.T) {
	var c Controller
	m := NewManager(&c)
	defer m.Close()
	prepare := func(context.Context) (Evaluator, io.Closer, error) {
		t.Error("off runtime prepared")
		return nil, nil, nil
	}
	m.Configure(Options{Budget: time.Second}, prepare)
	if m.Status().Phase != "off" {
		t.Fatal(m.Status())
	}
}

type watchedCloser struct {
	countCloser
	done chan struct{}
}

func (c *watchedCloser) Done() <-chan struct{} { return c.done }

func TestManagerRuntimeExitDisablesOptionalCalls(t *testing.T) {
	var controller Controller
	m := NewManager(&controller)
	defer m.Close()
	owned := &watchedCloser{done: make(chan struct{})}
	m.Configure(Options{Budget: time.Second, VoiceWork: true}, func(context.Context) (Evaluator, io.Closer, error) {
		return evaluatorFunc(func(context.Context, Request) (Response, error) { t.Error("dead runtime used"); return Response{}, nil }), owned, nil
	})
	waitPhase(t, m, "ready")
	close(owned.done)
	waitPhase(t, m, "unavailable")
	if controller.Enabled(VoiceWork) || m.Status().Err == nil {
		t.Fatal("runtime exit did not disable controller with a diagnostic")
	}
	if attempt := controller.Start(t.Context(), VoiceWork, WorkInput{Text: "List files", EnabledTiers: []Tier{Simple}}); attempt != nil {
		attempt.Close()
		t.Fatal("dead runtime accepted a turn")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if owned.closed.Load() != 1 {
		t.Fatal("runtime exit lost owned closer")
	}
}
