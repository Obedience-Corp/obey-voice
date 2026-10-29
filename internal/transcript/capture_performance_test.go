package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Opt-in measurement, separate from correctness tests. Timing includes the
// actual synchronous Bus.Emit observer dispatch/copy/reservation, never disk I/O.
func TestCaptureCallbackPopulation(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_CAPTURE_CALLBACK_ROOT")
	if root == "" {
		t.Skip("opt-in callback population")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute evidence root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "events.jsonl")
	writer, err := NewCaptureWriter(path, CaptureOptions{CaptureID: "callback-population", ContentPolicy: "talk"})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	detach := writer.Observe(bus)
	segments := make([]events.SegmentMetric, 8)
	for i := range segments {
		segments[i] = events.SegmentMetric{Index: i, Chars: 160, Provider: "controlled-provider", Tier: "controlled-tier", Place: "local", Error: strings.Repeat("diagnostic ", 16)}
	}
	durations := make([]int64, 1000)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for i := range durations {
		<-tick.C
		var event events.Event
		switch i % 4 {
		case 0:
			event = events.TurnMetrics{Outcome: "completed", Segments: segments}
		case 1:
			event = events.ResponseDelta{Text: strings.Repeat("representative response ", 128)}
		case 2:
			event = events.UserInput{Text: strings.Repeat("representative input ", 64)}
		case 3:
			zero := float64(i)
			event = events.PipelineTrace{Trace: &events.TraceContext{RuntimeID: "controlled-runtime", MonoMS: &zero}, Span: events.TraceSpan{ID: fmt.Sprintf("span-%d", i), Stage: "model", State: "end", StartMS: &zero, EndMS: &zero, Status: "completed"}}
		}
		start := time.Now()
		bus.Emit(event)
		durations[i] = time.Since(start).Nanoseconds()
	}
	detach()
	closeErr := writer.Close()
	health := writer.Health()
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	report := map[string]any{"measurement": "Bus.Emit entry to return, one actual capture observer; bounded copy/reservation included; disk async", "rate_hz": 100, "n": len(durations), "sample_ns": durations, "p50_ns": sorted[499], "p95_ns": sorted[949], "max_ns": sorted[999], "budget_p95_ns": 250000, "health": health, "close_error": fmt.Sprint(closeErr), "payload_mix": "8-segment metrics; 3KiB response; 1.4KiB input; immutable trace span, equal frequency"}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "callback-report.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if health.Dropped != 0 || health.WriteErrors != 0 {
		t.Fatalf("capture health: %+v", health)
	}
	if sorted[949] > 250000 {
		t.Fatalf("callback p95=%dns exceeds250µs; raw population retained", sorted[949])
	}
	t.Logf("callback p95=%dns n=%d", sorted[949], len(durations))
}
