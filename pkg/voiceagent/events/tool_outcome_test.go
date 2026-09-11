package events

import (
	"testing"
	"time"
)

func TestToolOutcomeFactsPreferTypedFields(t *testing.T) {
	code := 0
	e := ToolCallFinished{
		ID:      "call-live",
		JobID:   "",
		Preview: "stale preview",
		Err:     "stale error",
		Elapsed: time.Second,
		Outcome: &ToolOutcome{
			Status:        ToolOutcomeSucceeded,
			ExitCode:      &code,
			HTTPStatus:    nil,
			Elapsed:       250 * time.Millisecond,
			Reason:        "",
			OutputPreview: "héllo\nsecond line",
			OutputBytes:   19,
			Truncated:     true,
		},
	}
	if got := e.OutcomeStatus(); got != ToolOutcomeSucceeded {
		t.Fatalf("OutcomeStatus = %q, want typed succeeded despite legacy Err", got)
	}
	if got := e.ResultPreview(); got != "héllo\nsecond line" {
		t.Fatalf("ResultPreview = %q, want typed preview", got)
	}
	if got := e.ResultElapsed(); got != 250*time.Millisecond {
		t.Fatalf("ResultElapsed = %v, want typed elapsed", got)
	}
	if got := e.ResultReason(); got != "" {
		t.Fatalf("ResultReason = %q, want typed empty reason", got)
	}
	if e.JobID != "" || e.ID != "call-live" {
		t.Fatalf("live identity changed: id=%q job=%q", e.ID, e.JobID)
	}

	e.JobID = "job-1"
	if e.JobID != "job-1" || e.ID != "call-live" {
		t.Fatalf("job identity changed: id=%q job=%q", e.ID, e.JobID)
	}
}

func TestToolOutcomeStatusUnknownForOpaqueAndInvalidValues(t *testing.T) {
	cases := []ToolCallFinished{
		{},
		{Err: "legacy failure"},
		{Outcome: &ToolOutcome{}},
		{Outcome: &ToolOutcome{Status: "bogus"}},
		{Outcome: &ToolOutcome{Status: ToolOutcomeUnknown}},
	}
	for i, tc := range cases {
		if got := tc.OutcomeStatus(); got != ToolOutcomeUnknown && i != 1 {
			t.Errorf("case %d status=%q, want unknown", i, got)
		}
	}
	if got := (ToolCallFinished{Outcome: &ToolOutcome{Status: ToolOutcomeTimedOut}}).OutcomeStatus(); got != ToolOutcomeTimedOut {
		t.Fatalf("timed out status=%q", got)
	}
	if got := (ToolCallFinished{Outcome: &ToolOutcome{Status: ToolOutcomeCanceled}}).OutcomeStatus(); got != ToolOutcomeCanceled {
		t.Fatalf("canceled status=%q", got)
	}
}
