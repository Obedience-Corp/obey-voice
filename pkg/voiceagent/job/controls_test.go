package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDisablePreservesAdmittedQueueAndCanReenable(t *testing.T) {
	table := NewTable(t.Context())
	t.Cleanup(table.AbortAll)
	started, release := make(chan struct{}), make(chan struct{})
	first, err := table.Start(t.Context(), Spec{Task: "first", Provider: "ollama", Model: "original",
		Run: func(ctx context.Context) (string, error) {
			close(started)
			select {
			case <-release:
				return "first result", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	secondRan := make(chan struct{})
	_, err = table.Start(t.Context(), Spec{Task: "queued", Run: func(context.Context) (string, error) { close(secondRan); return "queued result", nil }})
	if err != nil {
		t.Fatal(err)
	}
	table.SetEnabled(false)
	if _, err := table.Start(t.Context(), Spec{Task: "not admitted"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Start off = %v", err)
	}
	if len(table.Status()) != 2 || !table.Active() {
		t.Fatal("disable changed existing work")
	}
	close(release)
	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("disabled admission stranded the existing queue")
	}
	table.SetEnabled(true)
	if _, err := table.Start(t.Context(), Spec{Task: "enabled again", Run: func(context.Context) (string, error) { return "ok", nil }}); err != nil {
		t.Fatal(err)
	}
	for _, info := range table.Status() {
		if info.ID == first && (info.Provider != "ollama" || info.Model != "original" || info.Result != "first result") {
			t.Fatalf("metadata changed: %+v", info)
		}
	}
}

func TestAbortAllNeverStartsQueuedJobs(t *testing.T) {
	table := NewTable(t.Context())
	started, canceled := make(chan struct{}), make(chan struct{})
	_, err := table.Start(t.Context(), Spec{Task: "running", Run: func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	var calls atomic.Int32
	for range 20 {
		_, err := table.Start(t.Context(), Spec{Task: "queued", Run: func(context.Context) (string, error) { calls.Add(1); return "", nil }})
		if err != nil {
			t.Fatal(err)
		}
	}
	table.AbortAll()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("running context not canceled")
	}
	for _, info := range table.Status() {
		if info.State != StateAborted {
			t.Fatalf("Stop all left %+v", info)
		}
	}
	if table.Active() || calls.Load() != 0 {
		t.Fatal("queued work ran during stop all")
	}
	if err := table.CheckAdmission(); err != nil {
		t.Fatal("stop all disabled future work")
	}
}
