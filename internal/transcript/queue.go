package transcript

import (
	"reflect"
	"time"
)

// isDurableRecord reports terminal / harness-critical record types that must
// outrank streaming noise under back-pressure (same idea as the TUI bridge).
func isDurableRecord(r record) bool {
	switch r.Type {
	case "header", "user", "agent", "error", "tool_call", "tool_result",
		"metrics", "tokens", "session_warning", "compacted", "cleared",
		"turn_interrupted", "work_state_changed", "work_delivery", "dropped", "trace_span", "capture_end", "capture_health", "record_omitted":
		return true
	default:
		return false
	}
}

// enqueue stamps and queues one record; it never blocks the emitting
// goroutine. The bus outlives conversations, so post-Close events are dropped
// silently rather than written to a closed file.
func (w *Writer) enqueue(r record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.limited.Load() {
		return
	}
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	r.Seq = w.seq.Add(1)
	if w.capture != nil {
		if r.Trace == nil {
			trace := w.capture.Trace
			trace.TurnID = nil
			trace.ParentTurnID = nil
			trace.ResponseID = nil
			trace.PersonaID = nil
			trace.TurnStartMS = nil
			if !trace.Origin.IsZero() {
				ms := float64(time.Since(trace.Origin)) / float64(time.Millisecond)
				trace.MonoMS = &ms
			}
			r.Trace = &trace
		}
		trace := *r.Trace
		if trace.RuntimeID == "" && trace.SessionID == "" && (r.Type == "capture_health" || r.Type == "capture_end") {
			trace = w.capture.Trace
		}
		trace.CaptureID = w.capture.CaptureID
		r.Trace = &trace
		size, ok := boundedSize(reflect.ValueOf(r), 0)
		if !ok {
			w.skipped.Add(1)
			r = record{Type: "record_omitted", TS: r.TS, Seq: r.Seq, Trace: r.Trace, Message: "payload exceeds bounded copy budget", SizeEstimateBytes: size, OmittedType: r.Type, SizeEstimateKind: sizeEstimateKind(size)}
			size = 4096
		}
		r.size = size

		w.queuedBytes += size
	}

	if w.capture == nil || w.queuedBytes <= MaxQueuedBytes-3*MaxRecordBytes-captureReserve {
		select {
		case w.ch <- r:
			return
		default:
		}

	}
	// Channel full: prefer durable records over agent_delta / speaking / etc.
	// Mirror the TUI bridge — drop oldest non-durable first, then oldest durable.
	q := make([]record, 0, cap(w.ch)+1)
	for {
		select {
		case m := <-w.ch:
			q = append(q, m)
		default:
			goto drained
		}
	}
drained:
	q = append(q, r)
	if w.capture == nil {
		before := len(q)
		q = fitTranscriptQueue(q, cap(w.ch))
		w.dropped.Add(int64(before - len(q)))
	} else {
		for len(q) > cap(w.ch) || w.queuedBytes > MaxQueuedBytes-3*MaxRecordBytes-captureReserve {
			if len(q) == 0 {
				break
			}
			drop := 0
			for i, m := range q {
				if !isDurableRecord(m) {
					drop = i
					break
				}
			}
			m := q[drop]
			w.queuedBytes -= m.size
			w.lostLocked(m.Seq)
			q = append(q[:drop], q[drop+1:]...)
		}
	}
	for _, m := range q {
		select {
		case w.ch <- m:
		default:
			// Should be impossible after fit; count rather than block.
			w.dropped.Add(1)
		}
	}
}

// fitTranscriptQueue keeps at most capacity records, dropping oldest
// non-durable first. If only durable events remain, the oldest durable drops.
func fitTranscriptQueue(q []record, capacity int) []record {
	if capacity <= 0 {
		return nil
	}
	for len(q) > capacity {
		drop := -1
		for i, m := range q {
			if !isDurableRecord(m) {
				drop = i
				break
			}
		}
		if drop < 0 {
			q = q[1:]
			continue
		}
		q = append(q[:drop], q[drop+1:]...)
	}
	return q
}
