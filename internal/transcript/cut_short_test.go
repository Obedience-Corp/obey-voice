package transcript

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestRecordsCarryAContinuedReplyAndAKnowinglyIncompleteJob(t *testing.T) {
	reply, _ := encodeEvent(events.ResponseReady{Response: "Done.", ContinuedAs: "job-1"})
	if reply.ContinuedAs != "job-1" || reply.Interrupted {
		t.Fatalf("reply record = %+v", reply)
	}
	for _, cut := range []bool{false, true} {
		job, _ := encodeEvent(events.JobFinished{ID: "job-1", CutShort: cut})
		if job.CutShort != cut || job.Reason != "" {
			t.Fatalf("job record = %+v", job)
		}
	}
	job, _ := encodeEvent(events.JobFinished{ID: "job-1", CutShort: true, CutShortReason: "time limit nearly reached"})
	if !job.CutShort || job.Reason != "time limit nearly reached" {
		t.Fatalf("job record = %+v", job)
	}
}

func TestCorrectionRecordsKeepTheirIdentity(t *testing.T) {
	first, _ := encodeEvent(events.JobCorrection{ID: "job-1", Revision: 1, Text: "Use the other file.", State: "pending", Brief: true})
	second, _ := encodeEvent(events.JobCorrection{ID: "job-1", Revision: 2, Text: "Use the other file.", State: "pending"})
	if first.ID != "job-1#1" || first.Kind != "brief" || second.ID != "job-1#2" || second.Kind != "" || first.State != "correction_pending" {
		t.Fatalf("records = %+v, %+v", first, second)
	}
}

func TestReportRecordsKeepTheCheckpoint(t *testing.T) {
	answered, ok := encodeEvent(events.JobReport{ID: "job-1", Checkpoint: "read 2 files", Answer: true})
	only, _ := encodeEvent(events.JobReport{ID: "job-2", Checkpoint: "ran 3 commands"})
	if !ok || answered.JobID != "job-1" || answered.Text != "read 2 files" || answered.State != "report_requested" || answered.Kind != "" || only.Kind != "checkpoint_only" {
		t.Fatalf("records = %+v, %+v", answered, only)
	}
	if !recordedEventTypes["job_report"] {
		t.Fatal("job_report is not recorded")
	}
}
