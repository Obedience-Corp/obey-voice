package transcript

import (
	"context"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"reflect"
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
	var captured record
	if err := json.Unmarshal(records[1], &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Span == nil || captured.Span.StartMS == nil || captured.Span.EndMS == nil || *captured.Span.StartMS != 0 || *captured.Span.EndMS != 0 {
		t.Fatalf("mutable span evidence: %+v", captured.Span)
	}
	if captured.Trace == nil || captured.Trace.RuntimeID != trace.RuntimeID {
		t.Fatalf("wrong scope evidence: %+v", captured.Trace)
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

func TestLiveWriterProjectsModelRequests(t *testing.T) {
	var mu sync.Mutex
	var records [][]byte
	trace := events.TraceContext{RuntimeID: "current", ConversationEpoch: "epoch"}
	w := NewLiveWriter(CaptureOptions{CaptureID: "live", Trace: trace}, func(raw []byte) { mu.Lock(); defer mu.Unlock(); records = append(records, raw) })
	start, prefill, prompt := 12.5, 40.0, 120
	w.Handle(events.PipelineTrace{Trace: &trace, Span: events.TraceSpan{ID: "request", Stage: "model_request", Lane: "model", StartMS: &start, State: "end", Status: "failed",
		Request: &events.TraceModelRequest{Purpose: "route", Provider: "ollama", Model: "fixture", PromptTokens: &prompt, PrefillMS: &prefill}}})
	w.Handle(events.PipelineTrace{Trace: &trace, Span: events.TraceSpan{ID: "turn", Stage: "turn", Lane: "turn", StartMS: &start, State: "start", Status: "running"}})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 4 {
		t.Fatalf("expected header, two spans and footer: %d", len(records))
	}
	var request struct {
		Span struct {
			Request map[string]any `json:"request"`
		} `json:"span"`
	}
	if err := json.Unmarshal(records[1], &request); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"purpose": "route", "provider": "ollama", "model": "fixture", "first_token_ms": nil, "prompt_tokens": float64(120), "output_tokens": nil, "load_ms": nil, "prefill_ms": float64(40), "generation_ms": nil}
	if !reflect.DeepEqual(request.Span.Request, want) {
		t.Fatalf("projected request = %v\nwant %v", request.Span.Request, want)
	}
	if strings.Contains(string(records[2]), `"request"`) {
		t.Fatalf("an ordinary span gained a request object: %s", records[2])
	}
}
