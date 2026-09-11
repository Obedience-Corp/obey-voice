package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"

// WorkStateChanged carries an immutable conversation work snapshot. It is a
// diagnostic projection; the coordinator and normal session checkpoint own
// live and durable state respectively. Receiving this event is not delivery.
type WorkStateChanged struct {
	Snapshot work.Snapshot
}

func (WorkStateChanged) eventType() string { return "work_state_changed" }

// WorkDelivery records one host boundary. Result IDs refer to immutable
// canonical outcomes; this diagnostic event itself grants no authority.
type WorkDelivery struct {
	Scope        work.WorkScope `json:"scope"`
	AttemptID    string         `json:"attempt_id"`
	TurnID       string         `json:"turn_id"`
	CaptureEpoch uint64         `json:"capture_epoch"`
	Stage        string         `json:"stage"`
	ResultIDs    []string       `json:"result_ids"`
	Reason       string         `json:"reason,omitempty"`
	Mode         string         `json:"mode,omitempty"`
	Segments     int            `json:"segments,omitempty"`
}

func (WorkDelivery) eventType() string { return "work_delivery" }
