package netapi

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestEnvelopeCarriesAContinuedReplyAndAKnowinglyIncompleteJob(t *testing.T) {
	reply := encodeEvent(events.ResponseReady{Response: "Done.", ContinuedAs: "job-1"})
	if reply["continued_as"] != "job-1" || reply["interrupted"] != false {
		t.Fatalf("reply envelope = %v", reply)
	}
	if _, ok := encodeEvent(events.ResponseReady{Response: "Done."})["continued_as"]; ok {
		t.Fatal("an ordinary reply carries continued_as")
	}
	if encodeEvent(events.JobFinished{ID: "job-1", CutShort: true})["cut_short"] != true {
		t.Fatal("a knowingly incomplete job does not say so")
	}
	if _, ok := encodeEvent(events.JobFinished{ID: "job-1"})["cut_short"]; ok {
		t.Fatal("a complete job carries cut_short")
	}
	forced := encodeEvent(events.JobFinished{ID: "job-1", CutShort: true, CutShortReason: "repeated the same tool call"})
	if forced["cut_short"] != true || forced["cut_short_reason"] != "repeated the same tool call" {
		t.Fatalf("forced answer envelope = %v", forced)
	}
	if _, ok := encodeEvent(events.JobFinished{ID: "job-1", CutShort: true})["cut_short_reason"]; ok {
		t.Fatal("a job cut short for another reason carries cut_short_reason")
	}
}
