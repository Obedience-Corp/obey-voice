package transcript

import (
	"encoding/json"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// NewLiveWriter reuses the capture schema and bounded event queue without a
// file or audio tap. publish runs on the writer goroutine and must not block.
func NewLiveWriter(opts CaptureOptions, publish func([]byte)) *Writer {
	w := &Writer{capture: &opts, ch: make(chan record, queueCapacity-2), done: make(chan struct{})}
	go w.runLive(publish)
	trace := opts.Trace
	trace.CaptureID = opts.CaptureID
	w.enqueue(record{Type: "header", V: Version, Trace: &trace, TraceSchema: events.TraceSchema, ClockOrigin: "runtime_start", ContentPolicy: "live", Cut: "recent_history", Environment: opts.Environment})
	return w
}

func (w *Writer) runLive(publish func([]byte)) {
	defer close(w.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	emit := func(r record) {
		raw, err := json.Marshal(r)
		if err != nil || len(raw) > MaxRecordBytes {
			w.skipped.Add(1)
			return
		}
		publish(raw)
		w.written.Store(r.Seq)
	}
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				health := w.Health()
				trace := w.capture.Trace
				trace.CaptureID = w.capture.CaptureID
				emit(record{Type: "capture_end", TS: time.Now().UTC().Format(time.RFC3339Nano), Seq: w.seq.Add(1), Trace: &trace, Health: &health, Status: "ended"})
				return
			}
			emit(r)
			w.mu.Lock()
			w.queuedBytes -= r.size
			w.mu.Unlock()
		case <-ticker.C:
			health := w.Health()
			if health.Dropped > 0 || health.Skipped > 0 || health.ScopeExcluded > 0 {
				w.enqueue(record{Type: "capture_health", Health: &health})
			}
		}
	}
}
