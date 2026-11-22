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
