package audio

import "context"

// OutputResetter is an optional local-output capability. Reset cancels pending
// audio and releases the device; the next playback opens it again. It does not
// replace the conversation, providers, volume, or shared capture frontend.
type OutputResetter interface {
	ResetOutput(context.Context) error
}

// OutputStatus distinguishes a completed reset (idle) from a successful reopen
// (ready). Backend failures remain visible until a retry succeeds.
type OutputStatus struct {
	Generation       uint64           `json:"generation"`
	State            string           `json:"state"`
	RecoveryReason   string           `json:"recovery_reason,omitempty"`
	LastError        string           `json:"last_error,omitempty"`
	RecoveryAttempts int              `json:"recovery_attempts"`
	Device           NegotiatedDevice `json:"device"`
}

type OutputStatusProvider interface {
	OutputStatus() OutputStatus
}
