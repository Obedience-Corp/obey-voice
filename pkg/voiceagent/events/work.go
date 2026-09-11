package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"

// WorkStateChanged carries an immutable conversation work snapshot. It is a
// diagnostic projection; the coordinator and normal session checkpoint own
// live and durable state respectively. Receiving this event is not delivery.
type WorkStateChanged struct {
	Snapshot work.Snapshot
}

func (WorkStateChanged) eventType() string { return "work_state_changed" }
