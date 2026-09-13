package delegation

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerAdmissionPermissionSurvivesSessionRotation(t *testing.T) {
	client := newFake()
	runtime, err := New(t.Context(), client, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	var disabled atomic.Bool
	disabled.Store(true)
	denied := errors.New("workers disabled")
	runtime.SetAdmissionCheck(func() error {
		if disabled.Load() {
			return denied
		}
		return nil
	})
	if _, err := runtime.Start("inspect the repo"); !errors.Is(err, denied) {
		t.Fatalf("Start = %v", err)
	}
	if len(runtime.Tasks()) != 0 {
		t.Fatal("rejected task appeared in status")
	}
	next := runtime.NewSession()
	t.Cleanup(func() { _ = next.Close() })
	if _, err := next.Start("inspect again"); !errors.Is(err, denied) {
		t.Fatal("clear reenabled workers")
	}
	disabled.Store(false)
	ticket, err := next.Start("allowed task")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("enabled worker never started")
	}
	disabled.Store(true)
	if len(next.Tasks()) != 1 || next.Tasks()[0].Ticket != ticket {
		t.Fatal("disable lost admitted task")
	}
	if err := next.Cancel(ticket); err != nil {
		t.Fatal(err)
	}
}
