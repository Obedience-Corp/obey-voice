package events

import "time"

// DecisionEvaluated reports an optional opportunity without retaining input,
// conversation, credentials or raw model output. Proposed is not worker
// admission: route.decided carries the host's accepted/rejected result.
type DecisionEvaluated struct {
	Trace *TraceContext `json:"trace,omitempty"`

	Point       string        `json:"point"`
	Disposition string        `json:"disposition"`
	Model       string        `json:"model,omitempty"`
	Tier        string        `json:"tier,omitempty"`
	Elapsed     time.Duration `json:"elapsed_ns"`
}

func (DecisionEvaluated) eventType() string { return "decision.evaluated" }
