package transcript

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"path/filepath"
	"testing"
)

func TestTraceProjectionPreservesZeroMissingAndTiming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	trace := events.TraceContext{RuntimeID: "runtime", MonoMS: &zero, TurnStartMS: &zero}
	span := events.TraceSpan{ID: "span", Stage: "synthesis", StartMS: &zero, EndMS: nil, State: "start", Status: "running"}
	w.Handle(events.PipelineTrace{Trace: &trace, Span: span})
	timing := &events.TurnTiming{Origin: "finalized_input", Raw: events.TimingMilestones{FinalizedInputMS: &zero}}
	w.Handle(events.TurnMetrics{Trace: &trace, Timing: timing, AudibleSink: "local_speaker"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := readRecords(t, path)
	if len(got) != 3 || got[1].Span == nil || got[1].Span.StartMS == nil || *got[1].Span.StartMS != 0 || got[1].Span.EndMS != nil {
		t.Fatalf("zero/missing trace lost: %+v", got)
	}
	if got[2].Timing == nil || got[2].Timing.Raw.FinalizedInputMS == nil || *got[2].Timing.Raw.FinalizedInputMS != 0 || got[2].AudibleSink != "local_speaker" {
		t.Fatal("final timing evidence lost")
	}
}
