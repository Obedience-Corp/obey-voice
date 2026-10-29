package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"

// WorkerUsageRecorded is observational accounting, never a billing guarantee.
// Nil token/cost values mean unknown; counters cover adapter-owned actions only.
type WorkerUsageRecorded struct {
	Trace *TraceContext `json:"trace,omitempty"`

	Scope          work.WorkScope `json:"scope"`
	JobID          string         `json:"job_id"`
	Requests       *int           `json:"requests"`
	ToolCalls      *int           `json:"tool_calls"`
	Retries        *int           `json:"retries"`
	InputTokens    *int           `json:"input_tokens"`
	OutputTokens   *int           `json:"output_tokens"`
	ChargeMicroUSD *int64         `json:"charge_micro_usd"`
}

func (WorkerUsageRecorded) eventType() string { return "worker.usage" }

type WorkerLimitReached struct {
	Trace *TraceContext `json:"trace,omitempty"`

	Scope       work.WorkScope `json:"scope"`
	JobID       string         `json:"job_id"`
	Message     string         `json:"message"`
	Termination string         `json:"termination"`
}

func (WorkerLimitReached) eventType() string { return "worker.limit_reached" }

// WorkerChargeState identifies the durable accounting record. Observed CLI
// costs do not settle it: only authoritative reconciliation can do that.
type WorkerChargeState struct {
	Trace *TraceContext `json:"trace,omitempty"`

	Scope              work.WorkScope `json:"scope"`
	JobID              string         `json:"job_id"`
	RequestID          string         `json:"request_id"`
	State              string         `json:"state"`
	ChargeCapAvailable bool           `json:"charge_cap_available"`
}

func (WorkerChargeState) eventType() string { return "worker.charge" }
