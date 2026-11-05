package workjournal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type journalStoreFake struct {
	started       chan struct{}
	secondStarted chan struct{}
	thirdStarted  chan struct{}
	release       chan struct{}
	mu            sync.Mutex
	writes        int
	err           error
	failNext      bool
	captures      []Checkpoint
}

func (s *journalStoreFake) Write(_ []Record, checkpoint Checkpoint) (Health, error) {
	// checkpoint capture is intentionally synchronized with writes for tests
	// that inspect the durable projection after recovery.
	func() { s.mu.Lock(); defer s.mu.Unlock(); s.captures = append(s.captures, checkpoint) }()
	s.mu.Lock()
	s.writes++
	writeNumber := s.writes
	s.mu.Unlock()
	if s.started != nil {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	}
	if writeNumber == 2 && s.secondStarted != nil {
		select {
		case <-s.secondStarted:
		default:
			close(s.secondStarted)
		}
	}
	if writeNumber == 3 && s.thirdStarted != nil {
		select {
		case <-s.thirdStarted:
		default:
			close(s.thirdStarted)
		}
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	fail := s.failNext
	s.failNext = false
	err := s.err
	s.mu.Unlock()
	if fail {
		return Health{}, err
	}
	return Health{PersistedSeq: checkpoint.LastSeq}, nil
}
func (s *journalStoreFake) Close() error { return nil }
func testJournal(t *testing.T, st journalStorage, opts Options) *Journal {
	t.Helper()
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	c := work.NewCoordinator(scope)
	j := newJournal(c, NewNormalizer(nil), st, opts, nil)
	t.Cleanup(func() {
		select {
		case <-j.done:
		default:
			_ = j.Close(context.Background())
		}
	})
	return j
}

func TestJournalStalledWriteDoesNotBlockObserveAndCountsInflight(t *testing.T) {
	st := &journalStoreFake{started: make(chan struct{}), release: make(chan struct{})}
	j := testJournal(t, st, Options{MaxRecords: 2, SyncInterval: 5 * time.Millisecond})
	j.Observe(events.WorkerLimitReached{Scope: j.coordinator.Scope(), Message: "one"})
	<-st.started
	done := make(chan struct{})
	go func() {
		j.Observe(events.WorkerLimitReached{Scope: j.coordinator.Scope(), Message: "two"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Observe blocked on stalled write")
	}
	j.Observe(events.WorkerLimitReached{Scope: j.coordinator.Scope(), Message: "three"})
	h := j.Health()
	if !h.Incomplete || h.Dropped == 0 || len(h.Gaps) == 0 {
		t.Fatalf("health=%+v", h)
	}
	close(st.release)
}

func TestJournalFailedBatchRetainsGapAndLatestSnapshot(t *testing.T) {
	st := &journalStoreFake{err: errors.New("disk full"), failNext: true}
	// Exercise the failed-batch projection synchronously. A Write-start signal
	// precedes its result and cannot establish that flush has updated health.
	// Keep the background timer out of this explicitly driven flush.
	j := testJournal(t, st, Options{SyncInterval: time.Hour})
	j.record(Record{Type: "failed"}, j.coordinator.Scope(), nil)
	j.flush()
	h := j.Health()
	if !h.Incomplete || len(h.Gaps) == 0 {
		t.Fatalf("health=%+v", h)
	}
}

func TestJournalCloseHonorsContextWhileWriteStalled(t *testing.T) {
	st := &journalStoreFake{started: make(chan struct{}), release: make(chan struct{})}
	j := testJournal(t, st, Options{SyncInterval: 5 * time.Millisecond})
	j.record(Record{Type: "hold"}, j.coordinator.Scope(), nil)
	<-st.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := j.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close err=%v", err)
	}
	close(st.release)
	select {
	case <-j.done:
	case <-time.After(time.Second):
		t.Fatal("journal did not close")
	}
}

func TestJournalConcurrentObserveKeepsSequenceOrder(t *testing.T) {
	st := &journalStoreFake{release: make(chan struct{})}
	j := testJournal(t, st, Options{MaxRecords: 128, SyncInterval: time.Hour})
	scope := j.coordinator.Scope()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); j.Observe(events.WorkerLimitReached{Scope: scope, Message: "concurrent"}) }()
	}
	wg.Wait()
	j.mu.Lock()
	defer j.mu.Unlock()
	last := uint64(0)
	for _, r := range j.queue {
		if r.Seq <= last {
			t.Fatalf("sequence order %d after %d", r.Seq, last)
		}
		last = r.Seq
	}
	close(st.release)
}

func TestJournalOverflowHealthIsPublishedWhileWriteStalled(t *testing.T) {
	st := &journalStoreFake{started: make(chan struct{}), release: make(chan struct{})}
	health := make(chan Health, 1)
	j := func() *Journal {
		scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
		return newJournal(work.NewCoordinator(scope), NewNormalizer(nil), st, Options{MaxRecords: 1, SyncInterval: 5 * time.Millisecond}, func(h Health) {
			select {
			case health <- h:
			default:
			}
		})
	}()
	j.Observe(events.WorkerLimitReached{Scope: j.coordinator.Scope(), Message: "one"})
	<-st.started
	j.Observe(events.WorkerLimitReached{Scope: j.coordinator.Scope(), Message: "two"})
	select {
	case h := <-health:
		if !h.Incomplete || h.Dropped == 0 {
			t.Fatalf("health=%+v", h)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow health was not published")
	}
	close(st.release)
	_ = j.Close(context.Background())
}

func TestJournalRecoveryPreservesRevisionAndFailureGap(t *testing.T) {
	st := &journalStoreFake{started: make(chan struct{}), secondStarted: make(chan struct{}), thirdStarted: make(chan struct{}), err: errors.New("disk full")}
	j := testJournal(t, st, Options{SyncInterval: 5 * time.Millisecond})
	scope := j.coordinator.Scope()
	j.record(Record{Type: "one"}, scope, nil)
	<-st.started
	j.mu.Lock()
	st.failNext = true
	j.mu.Unlock()
	j.record(Record{Type: "two"}, scope, nil)
	<-st.secondStarted
	snap := j.coordinator.Snapshot(scope)
	snap.Revision = 2
	j.Observe(events.WorkStateChanged{Snapshot: snap})
	select {
	case <-st.thirdStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery flush did not start")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.captures) < 3 || st.captures[len(st.captures)-1].Snapshot.Revision != 2 {
		t.Fatalf("captures=%+v", st.captures)
	}
	h := j.Health()
	if len(h.Gaps) == 0 || !h.Incomplete {
		t.Fatalf("health=%+v", h)
	}
}
