package work

import (
	"sync"
	"testing"
)

func TestDiagnosticEnqueueOrderAndConversationFence(t *testing.T) {
	scope := WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	c := NewCoordinator(scope)
	var stamps []DiagnosticStamp
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			c.RecordDiagnostic(scope, func(s DiagnosticStamp) { stamps = append(stamps, s); _ = c.Snapshot(scope) })
		})
	}
	wg.Wait()
	for i, s := range stamps {
		if s.Seq != uint64(i+1) {
			t.Fatalf("stamp %d = %d", i, s.Seq)
		}
	}
	if len(stamps) != 100 {
		t.Fatalf("got %d stamps", len(stamps))
	}
	if err := c.Retire(scope); err != nil {
		t.Fatal(err)
	}
	next := scope
	next.ConversationEpoch = "next"
	if err := c.Reset(next); err != nil {
		t.Fatal(err)
	}
	if c.RecordDiagnostic(scope, func(DiagnosticStamp) { t.Error("stale callback") }) {
		t.Fatal("accepted stale scope")
	}
	c.RecordDiagnostic(next, func(s DiagnosticStamp) {
		if s.Seq != 101 {
			t.Fatalf("reset sequence=%d", s.Seq)
		}
	})
}
