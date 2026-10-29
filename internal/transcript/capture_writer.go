package transcript

import (
	"bufio"
	"encoding/json"
	"time"
)

// runCapture owns buffered capture I/O, durable progress, health and sealing.
func (w *Writer) runCapture() {
	buffer := bufio.NewWriterSize(w.f, 64<<10)
	flush := time.NewTicker(50 * time.Millisecond)
	defer flush.Stop()
	syncTick := time.NewTicker(time.Second)
	defer syncTick.Stop()
	lastHealth := Health{}
	lastExternalSeq := int64(0)
	lastHealthSeq := int64(0)
	pendingWritten := int64(0)
	flushBuffer := func() bool {
		if err := buffer.Flush(); err != nil {
			w.noteWriteError(err)
			return false
		}
		w.written.Store(pendingWritten)
		return true
	}
	lose := func(seq int64) { w.mu.Lock(); w.lostLocked(seq); w.mu.Unlock() }
	appendRecord := func(r record, terminal bool) {
		if r.Trace == nil {
			trace := w.capture.Trace
			trace.CaptureID = w.capture.CaptureID
			r.Trace = &trace
		}
		data, err := json.Marshal(r)
		if err != nil {
			w.noteWriteError(err)
			lose(r.Seq)
			return
		}
		if len(data)+1 > MaxRecordBytes {
			w.skipped.Add(1)
			lose(r.Seq)
			return
		}
		limit := MaxCaptureBytes - captureReserve
		if terminal {
			limit = MaxCaptureBytes
		}
		if w.bytes.Load()+int64(len(data)+1) > limit {
			w.limited.Store(true)
			lose(r.Seq)
			w.stopAccepting()
			return
		}
		n, err := buffer.Write(append(data, '\n'))
		w.bytes.Add(int64(n))
		if err != nil {
			w.noteWriteError(err)
			lose(r.Seq)
			return
		}
		pendingWritten = r.Seq
	}
	syncFile := func() {
		if !flushBuffer() {
			return
		}
		if err := w.f.Sync(); err != nil {
			w.noteWriteError(err)
			return
		}
		w.synced.Store(w.written.Load())
	}
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				health := w.Health()
				appendRecord(record{Type: "capture_end", TS: time.Now().UTC().Format(time.RFC3339Nano), Seq: w.seq.Add(1), Health: &health, Status: captureEndStatus(health)}, true)
				syncFile()
				return
			}
			if r.Type != "capture_health" {
				lastExternalSeq = r.Seq
			}
			appendRecord(r, false)
			w.mu.Lock()
			w.queuedBytes -= r.size
			w.mu.Unlock()
		case <-flush.C:
			flushBuffer()
		case <-syncTick.C:
			health := w.Health()
			if lastExternalSeq != lastHealthSeq || health.Dropped != lastHealth.Dropped || health.Skipped != lastHealth.Skipped || health.ScopeExcluded != lastHealth.ScopeExcluded || health.WriteErrors != lastHealth.WriteErrors || health.Limited != lastHealth.Limited {
				w.enqueue(record{Type: "capture_health", Health: &health})
				lastHealth = health
				lastHealthSeq = lastExternalSeq
			}
			syncFile()
		}
	}
}
