package events

import "time"

// ToolOutcomeStatus describes an observed invocation outcome. Process success
// does not establish the success of an HTTP request or the worker's task.
type ToolOutcomeStatus string

const (
	ToolOutcomeUnknown   ToolOutcomeStatus = "unknown"
	ToolOutcomeSucceeded ToolOutcomeStatus = "succeeded"
	ToolOutcomeFailed    ToolOutcomeStatus = "failed"
	ToolOutcomeTimedOut  ToolOutcomeStatus = "timed_out"
	ToolOutcomeCanceled  ToolOutcomeStatus = "canceled"
)

// ToolOutcome carries execution facts separately from model-facing tool text.
// Nil status codes are unobserved. Producers publish a completed value and
// must not mutate it after delivering the tool's terminal event.
type ToolOutcome struct {
	Status        ToolOutcomeStatus
	ExitCode      *int
	HTTPStatus    *int
	Signal        string
	Reason        string
	Elapsed       time.Duration
	InputSummary  string
	OutputPreview string
	OutputRef     string
	OutputBytes   int64
	// Truncated means OutputPreview omits output, including multiline/length
	// shortening or bounded capture. OutputBytes counts bytes before redaction.
	Truncated bool
}

// OutcomeStatus preserves explicit legacy errors while leaving opaque legacy
// completions unknown. An absent error alone cannot establish success.
func (e ToolCallFinished) OutcomeStatus() ToolOutcomeStatus {
	if e.Outcome != nil {
		switch e.Outcome.Status {
		case ToolOutcomeSucceeded, ToolOutcomeFailed, ToolOutcomeTimedOut, ToolOutcomeCanceled:
			return e.Outcome.Status
		default:
			return ToolOutcomeUnknown
		}
	}
	if e.Err != "" {
		return ToolOutcomeFailed
	}
	return ToolOutcomeUnknown
}

// ResultPreview returns the authoritative typed preview when present.
func (e ToolCallFinished) ResultPreview() string {
	if e.Outcome != nil {
		return e.Outcome.OutputPreview
	}
	return e.Preview
}

// ResultElapsed uses one duration across rendering, folding and persistence.
func (e ToolCallFinished) ResultElapsed() time.Duration {
	if e.Outcome != nil {
		return e.Outcome.Elapsed
	}
	return e.Elapsed
}

// ResultReason preserves opaque legacy error detail without overriding a typed
// outcome with a stale legacy error field.
func (e ToolCallFinished) ResultReason() string {
	if e.Outcome != nil {
		return e.Outcome.Reason
	}
	return e.Err
}
