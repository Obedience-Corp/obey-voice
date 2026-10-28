package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type testSink struct {
	block <-chan struct{}
	fail  bool
	mu    sync.Mutex
	data  []byte
}

func (s *testSink) Write(data []byte) (int, error) {
	if s.block != nil {
		<-s.block
	}
	if s.fail {
		return 0, io.ErrClosedPipe
	}
	s.mu.Lock()
	s.data = append(s.data, data...)
	s.mu.Unlock()
	return len(data), nil
}
func (s *testSink) Sync() error  { return nil }
func (s *testSink) Close() error { return nil }
func captureTestWriter(sink captureSink) *Writer {
	return &Writer{f: sink, capture: &CaptureOptions{CaptureID: "test"}, ch: make(chan record, queueCapacity-2), done: make(chan struct{})}
}

func TestCaptureBoundsAndLateObserver(t *testing.T) {
	unblock := make(chan struct{})
	sink := &testSink{block: unblock}
	w := captureTestWriter(sink)
	go w.run()
	bus := events.NewBus()
	detach := w.Observe(bus)
	for i := 0; i < 1500; i++ {
		bus.Emit(events.ResponseDelta{Text: strings.Repeat("x", 2048)})
	}
	bus.Emit(events.UserInput{Text: "terminal wins"})
	h := w.Health()
	if h.QueuedBytes > MaxQueuedBytes || h.Dropped == 0 || len(h.Missing) == 0 || len(h.Missing) > 128 {
		t.Fatalf("unbounded or invisible loss: %+v", h)
	}
	detach()
	w.StopAccepting()
	// Simulates an observer callback snapshotted before detach.
	w.Handle(events.UserInput{Text: "after stop"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := w.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked close = %v", err)
	}
	close(unblock)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sink.data), "after stop") || !strings.Contains(string(sink.data), "terminal wins") {
		t.Fatal("detach/priority policy violated")
	}
}
func TestCaptureOversizeAndFileLimit(t *testing.T) {
	sink := &testSink{}
	w := captureTestWriter(sink)
	w.Handle(events.UserInput{Text: strings.Repeat("x", MaxRecordBytes)})
	omitted := <-w.ch
	if omitted.OmittedType != "user_input" || omitted.SizeEstimateKind != "bounded_estimate" || omitted.SizeEstimateBytes == 0 {
		t.Fatalf("omission lost provenance: %+v", omitted)
	}
	w.ch <- omitted
	if w.skipped.Load() != 1 {
		t.Fatal("oversized record not omitted")
	}
	w.bytes.Store(MaxCaptureBytes - captureReserve - 8)
	go w.run()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !w.Health().Limited || w.Health().Dropped == 0 || !strings.Contains(string(sink.data), "capture_end") || !strings.Contains(string(sink.data), "incomplete") {
		t.Fatalf("limit not sealed visibly: %+v %s", w.Health(), sink.data)
	}
}
func TestCaptureDiskFailure(t *testing.T) {
	w := captureTestWriter(&testSink{fail: true})
	go w.run()
	w.Handle(events.UserInput{Text: "hello"})
	if err := w.Close(); err == nil {
		t.Fatal("I/O failure reported successful close")
	}
	if h := w.Health(); h.WriteErrors == 0 || h.SyncedSequence != 0 {
		t.Fatalf("disk health = %+v", h)
	}
}
func TestCaptureHeaderAndImmutableCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := NewCaptureWriter(path, CaptureOptions{CaptureID: "capture", ContentPolicy: "talk", Cut: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if cap(w.ch) != queueCapacity-2 {
		t.Fatalf("capture channel capacity=%d", cap(w.ch))
	}
	zero := 0.0
	trace := events.TraceContext{RuntimeID: "runtime", MonoMS: &zero}
	span := events.TraceSpan{ID: "id", StartMS: &zero}
	w.Handle(events.PipelineTrace{Trace: &trace, Span: span})
	zero = 100
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, path)
	if records[0].V != 2 || records[0].TraceSchema != events.TraceSchema || records[0].Trace.CaptureID != "capture" {
		t.Fatal("capture header lost")
	}
	if *records[1].Trace.MonoMS != 0 || *records[1].Span.StartMS != 0 {
		t.Fatal("queued event borrowed mutable pointer")
	}
	if records[len(records)-1].Type != "capture_end" {
		t.Fatal("missing footer")
	}
	for i := 1; i < len(records); i++ {
		if records[i].Seq <= records[i-1].Seq {
			t.Fatal("nonmonotonic capture sequence")
		}
	}
}

func TestCaptureBytePressureEvictsOnlyNecessaryNoise(t *testing.T) {
	w := captureTestWriter(&testSink{})
	// Fill close to the reserved payload budget with a single non-durable record
	// followed by terminal records. The incoming terminal should evict noise only.
	text := strings.Repeat("x", 10000)
	w.enqueue(record{Type: "agent_delta", Text: text})
	for {
		w.mu.Lock()
		before := w.queuedBytes
		w.mu.Unlock()
		size, _ := boundedSize(reflect.ValueOf(record{Type: "user", Text: text}), 0)
		if before+2*size > MaxQueuedBytes-3*MaxRecordBytes-captureReserve {
			break
		}
		w.enqueue(record{Type: "user", Text: text})
	}
	before := w.dropped.Load()
	w.enqueue(record{Type: "user", Text: strings.Repeat("y", 20000)})
	if drops := w.dropped.Load() - before; drops != 1 {
		t.Fatalf("byte pressure dropped %d records, want exactly one", drops)
	}
	foundNoise := false
	for len(w.ch) > 0 {
		if (<-w.ch).Type == "agent_delta" {
			foundNoise = true
		}
	}
	if foundNoise {
		t.Fatal("byte pressure retained old noise over terminal")
	}
}

func TestCaptureOversizedDiagnosticOwnsBoundedTrace(t *testing.T) {
	w := captureTestWriter(&testSink{})
	response, turn := "frozen-response", strings.Repeat("x", MaxRecordBytes*2)
	trace := &events.TraceContext{RuntimeID: strings.Repeat("r", MaxRecordBytes*2), ResponseID: &response, TurnID: &turn}
	w.Handle(events.UserInput{Trace: trace, Text: "small payload; huge source identity"})
	response = "mutated"
	record := <-w.ch
	if record.Type != "record_omitted" || record.Trace == trace || record.Trace.RuntimeID != "" || record.Trace.TurnID != nil || record.Trace.ResponseID == nil || *record.Trace.ResponseID != "frozen-response" {
		t.Fatal("omission retained oversized/mutable source identity")
	}
	if size, ok := boundedSize(reflect.ValueOf(record), 0); !ok || size > 64<<10 {
		t.Fatal("diagnostic exceeded bounded reserve", size, ok)
	}
}

func TestCaptureIdleScopeExclusionHealthDoesNotSelfRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writer, err := NewCaptureWriter(path, CaptureOptions{CaptureID: "health", Trace: events.TraceContext{RuntimeID: "current"}})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	waitHealth := func(excluded int64) int {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, matched := 0, false
			for _, line := range strings.Split(string(raw), "\n") {
				var row struct {
					Type   string `json:"type"`
					Health Health `json:"health"`
				}
				if json.Unmarshal([]byte(line), &row) == nil && row.Type == "capture_health" {
					count++
					if row.Health.ScopeExcluded == excluded {
						matched = true
					}
				}
			}
			if matched {
				return count
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("periodic scope health unavailable", excluded)
		return 0
	}
	first := waitHealth(0)
	writer.Handle(events.Error{Trace: &events.TraceContext{RuntimeID: "old"}, Message: "excluded"})
	second := waitHealth(1)
	if second != first+1 {
		t.Fatal("unexpected health emission count", first, second)
	}
	time.Sleep(1200 * time.Millisecond)
	if again := waitHealth(1); again != second {
		t.Fatal("health record recursively generated another", second, again)
	}
}
