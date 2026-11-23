package job

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
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
