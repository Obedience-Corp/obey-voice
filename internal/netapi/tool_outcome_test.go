package netapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestEncodeToolOutcomeRoundTripPreservesTypedFacts(t *testing.T) {
	code := 0
	status := events.ToolCallFinished{
		ID: "call-1", JobID: "job-1", Name: "run_command", Kind: "execute",
		Preview: "legacy preview", Err: "legacy error", Elapsed: 8 * time.Second,
		Outcome: &events.ToolOutcome{
			Status:        events.ToolOutcomeSucceeded,
			ExitCode:      &code,
			HTTPStatus:    nil,
			Elapsed:       1250 * time.Millisecond,
			Signal:        "",
			Reason:        "",
			InputSummary:  "printf ok",
			OutputPreview: "héllo\nsecond",
			OutputBytes:   7000,
			Truncated:     true,
		},
	}
	frame, err := json.Marshal(encodeEvent(status))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(frame, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["id"] != "call-1" || wire["job_id"] != "job-1" || wire["status"] != string(events.ToolOutcomeSucceeded) {
		t.Fatalf("identity/status=%v, want job scoped success", wire)
	}
	if wire["preview"] != "héllo\nsecond" || wire["message"] != "" || wire["elapsed_ms"] != float64(1250) || wire["output_bytes"] != float64(7000) || wire["truncated"] != true {
		t.Fatalf("typed wire facts=%v", wire)
	}
	if wire["exit_code"] != float64(0) || wire["http_status"] != nil {
		t.Fatalf("nullable status codes=%v, want exit 0 and null HTTP", wire)
	}

	for _, outcome := range []*events.ToolOutcome{
		{Status: events.ToolOutcomeTimedOut, Reason: "timeout", Elapsed: time.Second},
		{Status: events.ToolOutcomeCanceled, Reason: "cancel", Elapsed: 2 * time.Second},
		{Status: events.ToolOutcomeUnknown, Elapsed: 3 * time.Second},
		{Status: "invalid", Elapsed: 4 * time.Second},
	} {
		got := encodeEvent(events.ToolCallFinished{ID: "opaque", Outcome: outcome})
		if outcome.Status == "invalid" && got["status"] != string(events.ToolOutcomeUnknown) {
			t.Fatalf("invalid status wire=%v, want unknown", got["status"])
		}
	}
	legacy := encodeEvent(events.ToolCallFinished{ID: "legacy", Preview: "old", Err: "old error", Elapsed: 3 * time.Second})
	if legacy["status"] != string(events.ToolOutcomeFailed) || legacy["preview"] != "old" || legacy["message"] != "old error" || legacy["elapsed_ms"] != int64(3000) {
		t.Fatalf("legacy wire=%v, want additive failed compatibility", legacy)
	}
	unknown := encodeEvent(events.ToolCallFinished{ID: "unknown", Preview: "old", Elapsed: time.Second})
	if unknown["status"] != string(events.ToolOutcomeUnknown) {
		t.Fatalf("absent legacy error status=%v, want unknown", unknown["status"])
	}
}

func TestEncodeScopedToolEventPreservesWorkScope(t *testing.T) {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	got := encodeEvent(events.ToolCallStarted{ID: "call", JobID: "job", WorkScope: scope})
	if got["work_scope"] != scope {
		t.Fatalf("work_scope = %#v, want %+v", got["work_scope"], scope)
	}
}
