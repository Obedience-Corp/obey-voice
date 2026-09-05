package job

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParentContextTimeoutDoesNotAbortJob(t *testing.T) {
	t.Parallel()
	lifetime := context.Background()
	table := NewTable(lifetime)

	parent, parentCancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	done := make(chan struct{})

	table.OnDone(func(id string, state State, result string, err error) {
		if state != StateDone {
			t.Errorf("state = %q, want done", state)
		}
		close(done)
	})

	id, err := table.Start(parent, Spec{
		Task: "slow",
		Run: func(ctx context.Context) (string, error) {
			ran.Store(true)
			select {
			case <-time.After(80 * time.Millisecond):
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	parentCancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish after parent cancel")
	}
	if !ran.Load() {
		t.Fatal("job run function never executed")
	}
	st := table.Status()
	if len(st) != 1 || st[0].ID != id || st[0].State != StateDone {
		t.Fatalf("status = %+v, want done %s", st, id)
	}
}

func TestAbortCancelsRunningJob(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())

	started := make(chan struct{})
	done := make(chan State, 1)

	table.OnDone(func(_ string, state State, _ string, _ error) {
		done <- state
	})

	id, err := table.Start(context.Background(), Spec{
		Task: "work",
		Run: func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-started
	table.Abort(id)

	select {
	case state := <-done:
		if state != StateAborted {
			t.Fatalf("terminal state = %q, want aborted", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not abort")
	}
}

func TestAbortMissingIDIsNoOp(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	table.Abort("missing")
}

func TestStartAfterCloseErrors(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	table.Close()
	if _, err := table.Start(context.Background(), Spec{Task: "x"}); err == nil {
		t.Fatal("Start after Close = nil, want error")
	}
}

func TestSecondSpawnQueues(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())

	block := make(chan struct{})
	firstStarted := make(chan struct{})
	var orderMu sync.Mutex
	var order []string

	table.OnDone(func(id string, state State, _ string, _ error) {
		if state == StateDone {
			orderMu.Lock()
			order = append(order, id)
			orderMu.Unlock()
		}
	})

	id1, err := table.Start(context.Background(), Spec{
		Task: "first",
		Run: func(ctx context.Context) (string, error) {
			close(firstStarted)
			<-block
			return "one", nil
		},
	})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-firstStarted

	id2, err := table.Start(context.Background(), Spec{Task: "second", Run: func(ctx context.Context) (string, error) {
		return "two", nil
	}})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}

	st := table.Status()
	var queued int
	for _, info := range st {
		if info.State == StateQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("queued jobs = %d, want 1; status=%+v", queued, st)
	}

	close(block)
	deadline := time.After(3 * time.Second)
	for {
		orderMu.Lock()
		n := len(order)
		orderMu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			orderMu.Lock()
			got := append([]string(nil), order...)
			orderMu.Unlock()
			t.Fatalf("finished order = %v, want both jobs", got)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	orderMu.Lock()
	got := append([]string(nil), order...)
	orderMu.Unlock()
	if got[0] != id1 || got[1] != id2 {
		t.Fatalf("finish order = %v, want %s then %s", got, id1, id2)
	}
}

func TestFailedJobState(t *testing.T) {
	t.Parallel()
	table := NewTable(context.Background())
	done := make(chan State, 1)
	table.OnDone(func(_ string, state State, _ string, err error) {
		if err == nil {
			t.Error("want error on failed job")
		}
		done <- state
	})

	_, err := table.Start(context.Background(), Spec{
		Task: "fail",
		Run: func(context.Context) (string, error) {
			return "", errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case state := <-done:
		if state != StateFailed {
			t.Fatalf("state = %q, want failed", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish")
	}
}
