package transcript

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const (
	MaxCaptureBytes int64 = 256 << 20
	MaxQueuedBytes  int64 = 8 << 20
	MaxRecordBytes        = 1 << 20
	captureReserve  int64 = 64 << 10
)

type CaptureOptions struct {
	CaptureID, ContentPolicy, Cut string
	Trace                         events.TraceContext
	Environment                   map[string]string
}

type SequenceRange struct {
	First int64 `json:"first"`
	Last  int64 `json:"last"`
}
type Health struct {
	Dropped         int64           `json:"dropped"`
	ScopeExcluded   int64           `json:"scope_excluded"`
	Skipped         int64           `json:"skipped"`
	WriteErrors     int64           `json:"write_errors"`
	QueuedBytes     int64           `json:"queued_bytes"`
	WrittenSequence int64           `json:"last_written_seq"`
	SyncedSequence  int64           `json:"last_synced_seq"`
	Bytes           int64           `json:"bytes"`
	Limited         bool            `json:"limited"`
	Missing         []SequenceRange `json:"missing_ranges,omitempty"`
}

func NewCaptureWriter(path string, opts CaptureOptions) (*Writer, error) {
	return openWriter(path, queueCapacity-2, &opts)
}
func (w *Writer) Observe(bus *events.Bus) func() {
	if bus == nil {
		return func() {}
	}
	return bus.ObserveBeforeFanout(func(e events.Event) {
		if recordedEventTypes[events.EventType(e)] {
			w.Handle(e)
		}
	})
}
func (w *Writer) Health() Health {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Health{ScopeExcluded: w.scopeExcluded.Load(), Dropped: w.dropped.Load(), Skipped: w.skipped.Load(), WriteErrors: w.writeErrors.Load(), QueuedBytes: w.queuedBytes, WrittenSequence: w.written.Load(), SyncedSequence: w.synced.Load(), Bytes: w.bytes.Load(), Limited: w.limited.Load(), Missing: append([]SequenceRange(nil), w.missing...)}
}
func (w *Writer) lostLocked(seq int64) {
	w.dropped.Add(1)
	n := len(w.missing)
	if n > 0 && seq <= w.missing[n-1].Last+1 && seq >= w.missing[n-1].First {
		if seq > w.missing[n-1].Last {
			w.missing[n-1].Last = seq
		}
		return
	}
	w.missing = append(w.missing, SequenceRange{seq, seq})
	// Ordering may change when priority evicts an older queued event. Coalesce
	// conservatively: the enclosing range can overstate loss, never hide it.
	if len(w.missing) > 128 {
		first, last := w.missing[0].First, w.missing[0].Last
		for _, r := range w.missing {
			if r.First < first {
				first = r.First
			}
			if r.Last > last {
				last = r.Last
			}
		}
		w.missing = []SequenceRange{{first, last}}
	}
}

// boundedSize rejects before copying and before walking unbounded collections.
// Its conservative estimate includes pointer/container overhead and JSON escapes.
func boundedSize(v reflect.Value, depth int) (int64, bool) {
	if !v.IsValid() {
		return 8, true
	}
	if depth > 24 {
		return 0, false
	}
	var size int64 = 64
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return size, true
		}
		return boundedSize(v.Elem(), depth+1)
	case reflect.String:
		size += int64(v.Len()) * 6
	case reflect.Slice, reflect.Array:
		if v.Len() > 4096 {
			return 0, false
		}
		for i := 0; i < v.Len(); i++ {
			n, ok := boundedSize(v.Index(i), depth+1)
			if !ok {
				return size + n, false
			}
			size += n
			if size > MaxRecordBytes-captureReserve {
				return size, false
			}
		}
	case reflect.Map:
		if v.Len() > 128 {
			return 0, false
		}
		it := v.MapRange()
		for it.Next() {
			for _, item := range []reflect.Value{it.Key(), it.Value()} {
				n, ok := boundedSize(item, depth+1)
				if !ok {
					return size + n, false
				}
				size += n
			}
		}
	case reflect.Struct:
		// time.Time has private monotonic data, copied as a value.
		if v.Type() == reflect.TypeFor[time.Time]() {
			return 128, true
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue
			}
			n, ok := boundedSize(v.Field(i), depth+1)
			if !ok {
				return size + n, false
			}
			size += n
		}
	}
	return size, size <= MaxRecordBytes-captureReserve
}
func copyBounded(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(strings.Clone(v.String())).Convert(v.Type())
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(copyBounded(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(copyBounded(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(copyBounded(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(copyBounded(it.Key()), copyBounded(it.Value()))
		}
		return out
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() && v.Type().Field(i).PkgPath == "" {
				out.Field(i).Set(copyBounded(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
func (w *Writer) CloseContext(ctx context.Context) error {
	w.stopAccepting()
	select {
	case <-w.done:
		return w.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *Writer) stopAccepting() {
	w.once.Do(func() { w.mu.Lock(); w.closed = true; close(w.ch); w.mu.Unlock() })
}

// StopAccepting rejects even callbacks already snapshotted by the event bus.
func (w *Writer) StopAccepting() { w.stopAccepting() }

func captureEndStatus(health Health) string {
	if health.WriteErrors > 0 || health.Limited {
		return "incomplete"
	}
	return "sealed"
}

type captureSink interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func sizeEstimateKind(size int64) string {
	if size == 0 {
		return "unknown"
	}
	return "bounded_estimate"
}
