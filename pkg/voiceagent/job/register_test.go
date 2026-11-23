package job

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Whatever Register sets up for an id exists before any caller can see that
// id, so a status read, a cancel or a done callback always finds it.
func TestRegisterRunsBeforeTheJobIsVisible(t *testing.T) {
	table := NewTable(context.Background())
	defer table.Close()
	table.SetWorkerLimits(1, 64)
	hold := make(chan struct{})
	defer close(hold)
	if _, err := table.Start(context.Background(), Spec{Task: "hold", Run: func(context.Context) (string, error) { <-hold; return "", nil }}); err != nil {
		t.Fatal(err)
	}
	var registered sync.Map
	var seenEarly atomic.Bool
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, info := range table.Status() {
				if info.Task == "queued" {
					if _, ok := registered.Load(info.ID); !ok {
						seenEarly.Store(true)
					}
				}
			}
		}
	})
	for range 50 {
		var order []string
		id, err := table.Start(context.Background(), Spec{Task: "queued",
			Register:   func(id string) { registered.Store(id, true); order = append(order, "register") },
			OnAccepted: func(string) { order = append(order, "accepted") },
			Run:        func(context.Context) (string, error) { return "", nil }})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := registered.Load(id); !ok || len(order) != 2 || order[0] != "register" {
			t.Fatalf("id=%s order=%v", id, order)
		}
		table.Cancel(id)
	}
	close(stop)
	wg.Wait()
	if seenEarly.Load() {
		t.Fatal("a job was visible before it was registered")
	}
}

func TestOnEndingNamesTheCauseThatWon(t *testing.T) {
	table := NewTable(context.Background())
	defer table.Close()
	got := map[string]error{}
	var mu sync.Mutex
	start := func(task string) string {
		started := make(chan struct{})
		id, err := table.Start(context.Background(), Spec{Task: task,
			OnEnding: func(state State, err error) {
				mu.Lock()
				got[task] = err
				mu.Unlock()
			},
			Run: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		return id
	}
	table.Cancel(start("cancelled"))
	start("aborted")
	table.AbortAll()
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(got["cancelled"], ErrCancelled) || errors.Is(got["aborted"], ErrCancelled) || !errors.Is(got["aborted"], errAborted) {
		t.Fatalf("causes=%v", got)
	}
}

func TestCancelFreezesTheRunningTimeAtTheStop(t *testing.T) {
	table := NewTable(context.Background())
	defer table.Close()
	started := make(chan struct{})
	id, err := table.Start(context.Background(), Spec{Task: "run", Run: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	before, _ := table.Cancel(id)
	first := before.Elapsed()
	for at := time.Now(); !time.Now().After(at); {
	}
	if before.State != StateRunning || before.Elapsed() != first {
		t.Fatalf("state=%s elapsed moved from %v to %v", before.State, first, before.Elapsed())
	}
}

func TestPanickingRegisterLeavesTheTableUsable(t *testing.T) {
	table := NewTable(context.Background())
	defer table.Close()
	func() {
		defer func() { _ = recover() }()
		_, _ = table.Start(context.Background(), Spec{Task: "x", Register: func(string) { panic("register failed") }, Run: func(context.Context) (string, error) { return "", nil }})
	}()
	done := make(chan struct{})
	go func() { table.Status(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the table lock was left held")
	}
}

// A job's run function, and all it holds, is let go once the job is over.
func TestEndedJobReleasesItsRunFunction(t *testing.T) {
	table := NewTable(context.Background())
	defer table.Close()
	id, err := table.Start(context.Background(), Spec{Task: "run", Run: func(context.Context) (string, error) { return "done", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.AwaitExit(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	table.mu.Lock()
	defer table.mu.Unlock()
	if table.jobs[id].spec.Run != nil {
		t.Fatal("the ended job still holds its run function")
	}
}
