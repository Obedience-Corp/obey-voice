package transcript

import (
	"context"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLiveWriterCopiesEvidenceAndRejectsOtherRuntime(t *testing.T) {
	var mu sync.Mutex
	var records [][]byte
	trace := events.TraceContext{RuntimeID: "current", ConversationEpoch: "epoch"}
	w := NewLiveWriter(CaptureOptions{CaptureID: "live", Trace: trace}, func(raw []byte) { mu.Lock(); defer mu.Unlock(); records = append(records, raw) })
	zero := 0.0
	span := events.TraceSpan{ID: "stage", Stage: "model", StartMS: &zero, EndMS: &zero, State: "end", Status: "completed"}
	w.Handle(events.PipelineTrace{Trace: &trace, Span: span})
	zero = 999
	wrong := events.TraceContext{RuntimeID: "other"}
	w.Handle(events.UserInput{Text: "must exclude", Trace: &wrong})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 3 {
		t.Fatalf("expected header/span/footer: %d", len(records))
	}
	if strings.Contains(string(records[1]), "999") || strings.Contains(string(records[1]), "must exclude") {
		t.Fatal("mutable or wrong scope evidence")
	}
	var end struct {
		Health Health `json:"health"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(records[2], &end); err != nil {
		t.Fatal(err)
	}
	if end.Status != "ended" || end.Health.ScopeExcluded != 1 || end.Health.Bytes != 0 {
		t.Fatalf("wrong live health %+v", end)
	}
}

func TestLiveWriterPressureDoesNotBlockProducer(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w := NewLiveWriter(CaptureOptions{}, func([]byte) { once.Do(func() { close(entered) }); <-release })
	<-entered
	for range 1500 {
		w.Handle(events.ResponseDelta{Text: strings.Repeat("x", 8192)})
	}
	if h := w.Health(); h.Dropped == 0 || h.QueuedBytes > MaxQueuedBytes {
		t.Fatal("unbounded or invisible loss", h)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := w.CloseContext(ctx); err == nil {
		t.Fatal("expected bounded close")
	}
	close(release)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
