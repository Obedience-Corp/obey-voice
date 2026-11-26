package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"

// RouteReasonInlineWork is the reason code of a route refused because its
// persona works inline: no job started and no worker was involved.
const RouteReasonInlineWork = "inline_work"

// RejectedRouteLabel is how a rejected route is introduced to the user.
func RejectedRouteLabel(e RouteDecided) string {
	if e.ReasonCode == RouteReasonInlineWork {
		return "No job started"
	}
	return "Worker rejected"
}

// RouteDecided reports the host's accepted/rejected routing outcome. Provider,
// account and model identity remain in the frozen job details.
type RouteDecided struct {
	Trace *TraceContext `json:"trace,omitempty"`

	Scope      work.WorkScope `json:"scope"`
	Kind       string         `json:"kind"`
	Tier       string         `json:"tier,omitempty"`
	ReasonCode string         `json:"reason_code,omitempty"`
	State      string         `json:"state"`
	JobID      string         `json:"job_id,omitempty"`
	Task       string         `json:"task,omitempty"`
	Message    string         `json:"message,omitempty"`
	// Action is the job control a job.control settlement asked for: cancel,
	// report or steer. Empty on every other decision.
	Action string `json:"action,omitempty"`
}

func (RouteDecided) eventType() string { return "route.decided" }
