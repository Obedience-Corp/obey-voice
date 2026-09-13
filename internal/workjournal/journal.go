package workjournal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type journalStorage interface {
	Write([]Record, Checkpoint) (Health, error)
	Close() error
}

// Journal serializes diagnostics in memory and writes them on one goroutine.
// Queue limits include the batch currently in flight. Canonical work saving
// proceeds independently even when diagnostic storage is unavailable.
type Journal struct {
	coordinator                         *work.Coordinator
	normalizer                          *Normalizer
	storage                             journalStorage
	opts                                Options
	onHealth                            func(Health)
	mu                                  sync.Mutex
	queue                               []Record
	bytes, inFlightCount, inFlightBytes int
	latest                              Checkpoint
	health                              Health
	closed                              bool
	wake                                chan struct{}
	stop                                chan struct{}
	done                                chan struct{}
	healthWake                          chan struct{}
	healthDone                          chan struct{}
	once                                sync.Once
}

func New(dir string, c *work.Coordinator, cfg *config.Config, opts Options, onHealth func(Health)) *Journal {
	opts = opts.defaults()
	return newJournal(c, NewNormalizer(cfg), &retentionStorage{storage: newStorage(dir, c.Scope(), opts)}, opts, onHealth)
}

func newJournal(c *work.Coordinator, n *Normalizer, st journalStorage, opts Options, onHealth func(Health)) *Journal {
	j := &Journal{coordinator: c, normalizer: n, storage: st, opts: opts.defaults(), onHealth: onHealth,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), healthWake: make(chan struct{}, 1), healthDone: make(chan struct{})}
	j.latest = Checkpoint{Schema: Schema, Authority: "diagnostic_only", Projection: "bounded_redacted", Snapshot: j.normalizer.Snapshot(c.Snapshot(c.Scope()))}
	initial := j.latest.Snapshot
	j.record(Record{Type: "session.started", Scope: "session", Data: mustJSON(j.normalizer.Config())}, c.Scope(), &initial)
	go j.run()
	go j.runHealth()
	return j
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// Observe performs bounded memory work only and is suitable for the bus's
// before-fanout observer. Audio frames, tokens and hidden reasoning are ignored.
func (j *Journal) Observe(e events.Event) {
	if j == nil || !diagnosticEvent(e) {
		return
	}
	snapshot := j.coordinator.Snapshot(j.coordinator.Scope())
	r, ok := j.normalizer.Normalize(e, snapshot)
	if !ok {
		return
	}
	scope := snapshot.Scope
	if r.SessionID != "" {
		scope = work.WorkScope{SessionID: r.SessionID, RuntimeEpoch: r.RuntimeID, ConversationEpoch: r.ConversationEpoch}
	}
	var checkpoint *work.Snapshot
	if changed, ok := e.(events.WorkStateChanged); ok {
		safe := j.normalizer.Snapshot(changed.Snapshot)
		checkpoint = &safe
	}
	j.record(r, scope, checkpoint)
}

func (j *Journal) record(r Record, scope work.WorkScope, snapshot *work.Snapshot) {
	j.coordinator.RecordDiagnostic(scope, func(stamp work.DiagnosticStamp) {
		r.Schema, r.SessionID, r.RuntimeID, r.ConversationEpoch = Schema, stamp.Scope.SessionID, stamp.Scope.RuntimeEpoch, stamp.Scope.ConversationEpoch
		r.Seq, r.At, r.MonoMS, r.Revision = stamp.Seq, stamp.At, stamp.MonoMS, stamp.Revision
		if snapshot != nil {
			r.Revision = snapshot.Revision
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return
		} // normalized payloads are already valid JSON
		size := len(encoded) + 1
		if r.DetailPayload != nil {
			size += len(r.DetailPayload.Text)
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.closed {
			return
		}
		j.latest.LastSeq, j.latest.At = r.Seq, r.At
		if snapshot != nil && (snapshot.Scope != j.latest.Snapshot.Scope || snapshot.Revision >= j.latest.Snapshot.Revision) {
			j.latest.Snapshot = *snapshot
		}
		if len(j.queue)+j.inFlightCount >= j.opts.MaxRecords || j.bytes+j.inFlightBytes+size > j.opts.MaxQueueBytes {
			j.health.Incomplete = true
			j.health.Dropped++
			j.addGapLocked(r.Seq, r.Seq, "queue_overflow")
			j.notifyHealth()
		} else {
			j.queue = append(j.queue, r)
			j.bytes += size
		}
		select {
		case j.wake <- struct{}{}:
		default:
		}
	})
}

func (j *Journal) addGapLocked(from, to uint64, reason string) {
	last := len(j.health.Gaps) - 1
	if last >= 0 && j.health.Gaps[last].Reason == reason && j.health.Gaps[last].To+1 >= from {
		if to > j.health.Gaps[last].To {
			j.health.Gaps[last].To = to
		}
		return
	}
	// Keep missing-history bookkeeping bounded under an indefinitely broken
	// disk. Combining old ranges deliberately overstates, never hides, loss.
	if len(j.health.Gaps) >= 128 {
		j.health.Gaps[0] = Gap{RuntimeID: j.latest.Snapshot.Scope.RuntimeEpoch, From: j.health.Gaps[0].From, To: j.health.Gaps[1].To, Reason: "history_incomplete"}
		j.health.Gaps = append(j.health.Gaps[:1], j.health.Gaps[2:]...)
	}
	j.health.Gaps = append(j.health.Gaps, Gap{RuntimeID: j.latest.Snapshot.Scope.RuntimeEpoch, From: from, To: to, Reason: reason})
}

func cloneHealth(h Health) Health { h.Gaps = append([]Gap(nil), h.Gaps...); return h }
func (j *Journal) Health() Health { j.mu.Lock(); defer j.mu.Unlock(); return cloneHealth(j.health) }

func (j *Journal) flush() {
	j.mu.Lock()
	records, checkpoint := j.queue, j.latest
	j.inFlightCount, j.inFlightBytes = len(records), j.bytes
	j.queue, j.bytes = nil, 0
	checkpoint.Health = cloneHealth(j.health)
	j.mu.Unlock()
	health, err := j.storage.Write(records, checkpoint)
	j.mu.Lock()
	j.inFlightCount, j.inFlightBytes = 0, 0
	if err != nil {
		j.health.Incomplete = true
		j.health.LastError = j.normalizer.Text(err.Error(), 1024)
		// Failed batches are not retained without bound. Their missing range is
		// explicit even if a partial prefix reached the disk before the error.
		if len(records) > 0 {
			j.addGapLocked(records[0].Seq, records[len(records)-1].Seq, "storage_failure")
		}
	} else {
		j.health.LastError = j.normalizer.Text(health.LastError, 1024)
		j.health.PersistedSeq, j.health.PersistedAt = health.PersistedSeq, health.PersistedAt
		j.health.RetainedFrom = health.RetainedFrom
		j.health.Incomplete = j.health.Incomplete || health.Incomplete
	}
	j.mu.Unlock()
	j.notifyHealth()
}

func (j *Journal) notifyHealth() {
	select {
	case j.healthWake <- struct{}{}:
	default:
	}
}

// A separate coalescing dispatcher keeps disk stalls from hiding overflow.
// There is at most one queued wakeup, never one goroutine per event.
func (j *Journal) runHealth() {
	defer close(j.healthDone)
	for {
		select {
		case <-j.healthWake:
			if j.onHealth != nil {
				j.onHealth(j.Health())
			}
		case <-j.done:
			if j.onHealth != nil {
				j.onHealth(j.Health())
			}
			return
		}
	}
}

func (j *Journal) run() {
	defer close(j.done)
	ticker := time.NewTicker(j.opts.SyncInterval)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case <-j.wake:
			dirty = true
			j.mu.Lock()
			urgent := j.latest.Snapshot.Retired
			if n := len(j.queue); n > 0 {
				t := j.queue[n-1].Type
				urgent = urgent || t == "job.ended" || t == "result.published"
			}
			j.mu.Unlock()
			if urgent {
				j.flush()
				dirty = false
			}
		case <-ticker.C:
			if dirty || j.Health().LastError != "" {
				j.flush()
				dirty = false
			}
		case <-j.stop:
			j.mu.Lock()
			j.latest.Sealed = true
			j.mu.Unlock()
			j.flush()
			if err := j.storage.Close(); err != nil {
				j.mu.Lock()
				j.health.LastError = j.normalizer.Text(err.Error(), 1024)
				j.health.Incomplete = true
				j.mu.Unlock()
			}
			return
		}
	}
}

func (j *Journal) Close(ctx context.Context) error {
	if j == nil {
		return nil
	}
	j.once.Do(func() { j.mu.Lock(); j.closed = true; j.mu.Unlock(); close(j.stop) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-j.done:
		if j.healthDone != nil {
			select {
			case <-j.healthDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		h := j.Health()
		if h.LastError != "" {
			return errors.New(h.LastError)
		}
		return nil
	}
}
