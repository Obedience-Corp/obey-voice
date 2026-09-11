package transcript

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestWriterPersistsTypedToolOutcomeFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-outcomes.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	code := 0
	bus.Emit(events.ToolCallFinished{
		ID: "call-job", JobID: "job-1", Name: "run_command", Kind: "execute",
		Preview: "legacy", Err: "legacy stale", Elapsed: 9 * time.Second,
		Outcome: &events.ToolOutcome{
			Status:        events.ToolOutcomeSucceeded,
			ExitCode:      &code,
			Elapsed:       1500 * time.Millisecond,
			OutputPreview: "héllo\nsecond",
			OutputBytes:   9001,
			Truncated:     true,
		},
	})
	bus.Emit(events.ToolCallFinished{
		ID: "call-live", Name: "run_command", Kind: "execute",
		Outcome: &events.ToolOutcome{Status: events.ToolOutcomeTimedOut, Reason: "command timed out", Elapsed: 2 * time.Second},
	})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs := readRecords(t, path)
	if len(recs) != 3 {
		t.Fatalf("records=%d, want header plus two outcomes: %+v", len(recs), recs)
	}
	job := recs[1]
	if job.Type != "tool_result" || job.ID != "call-job" || job.JobID != "job-1" {
		t.Fatalf("job record=%+v, want scoped identity", job)
	}
	if job.Status != string(events.ToolOutcomeSucceeded) || job.IsError || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("job outcome=%+v, want successful exit 0", job)
	}
	if job.Preview != "héllo\nsecond" || job.Message != "" || job.ElapsedS != 1.5 || job.OutputBytes != 9001 || !job.Truncated {
		t.Fatalf("job facts=%+v, want typed fields and elapsed_s=1.5", job)
	}
	live := recs[2]
	if live.ID != "call-live" || live.JobID != "" || live.Status != string(events.ToolOutcomeTimedOut) || !live.IsError || live.Reason != "command timed out" || live.ElapsedS != 2 {
		t.Fatalf("live timeout=%+v, want timeout facts", live)
	}
}
