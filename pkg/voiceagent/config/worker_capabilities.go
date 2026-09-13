package config

// Capability is an observed boundary that may be enforced by a worker
// adapter. Unsupported boundaries are explicit so policy code cannot infer
// support from a provider name or SDK field.
type Capability struct {
	Supported bool   `json:"supported" yaml:"supported"`
	Layer     string `json:"layer,omitempty" yaml:"layer,omitempty"`
	Evidence  string `json:"evidence,omitempty" yaml:"evidence,omitempty"`
}

// WorkerCapabilities is the adapter capability record used by worker policy.
// Request means application-owned model requests; it deliberately does not
// claim that one CLI process equals one provider request.
type WorkerCapabilities struct {
	Provider string `json:"provider" yaml:"provider"`
	Adapter  string `json:"adapter" yaml:"adapter"`
	Version  string `json:"version" yaml:"version"`

	RequestBoundary       Capability `json:"request_boundary" yaml:"request_boundary"`
	TurnBoundary          Capability `json:"turn_boundary" yaml:"turn_boundary"`
	OutputTokenCeiling    Capability `json:"output_token_ceiling" yaml:"output_token_ceiling"`
	ToolCallMediation     Capability `json:"tool_call_mediation" yaml:"tool_call_mediation"`
	RetryLimit            Capability `json:"retry_limit" yaml:"retry_limit"`
	RuntimeLimit          Capability `json:"runtime_limit" yaml:"runtime_limit"`
	Usage                 Capability `json:"usage" yaml:"usage"`
	AuthoritativeBilling  Capability `json:"authoritative_billing" yaml:"authoritative_billing"`
	ChargeBound           Capability `json:"charge_bound" yaml:"charge_bound"`
	ProcessExit           Capability `json:"process_exit" yaml:"process_exit"`
	DescendantExit        Capability `json:"descendant_exit" yaml:"descendant_exit"`
	CancellationRequested Capability `json:"cancellation_requested" yaml:"cancellation_requested"`
	ResumablePreemption   Capability `json:"resumable_preemption" yaml:"resumable_preemption"`
}
