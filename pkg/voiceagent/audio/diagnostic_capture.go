package audio

import (
	"context"
	"time"
)

// DiagnosticCaptureStatus describes the explicit, bounded playback PCM capture.
type DiagnosticCaptureStatus struct {
	Active        bool      `json:"active"`
	Directory     string    `json:"directory,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Bytes         uint64    `json:"bytes"`
	DroppedEvents uint64    `json:"dropped_events"`
	LimitReason   string    `json:"limit_reason,omitempty"`
}

type DiagnosticCapturer interface {
	StartDiagnosticCapture(context.Context) (DiagnosticCaptureStatus, error)
	StopDiagnosticCapture() DiagnosticCaptureStatus
	DiagnosticCaptureStatus() DiagnosticCaptureStatus
}
