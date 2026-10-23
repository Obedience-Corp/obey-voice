package pipeline

import (
	"encoding/json"
	"log"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Copied at qualification, so the trace describes the decision that actually
// stopped playback, rather than a later speaker verdict or audio callback.
type interruptDiagnostic struct {
	SpeakerGate      string `json:"speaker_gate"`
	DuringArm        bool   `json:"during_arm"`
	NeuralChunks     int    `json:"neural_chunks"`
	Audible          bool   `json:"audible"`
	StartupQualified bool   `json:"startup_qualified"`
	NearEndDuringArm bool   `json:"near_end_during_arm"`
	Playing          bool   `json:"playing"`
}

// Enrolled-only requires a fresh positive match. Missing recognition must not
// admit playback echo as a human interruption. A nil source is anyone mode.
func (c *interruptController) speakerDecision() (bool, string) {
	if c.speakerVerdict == nil {
		return true, "not_configured"
	}
	v := c.speakerVerdict()
	if !v.Healthy {
		return false, "unhealthy"
	}
	if !v.HasVerdict {
		return false, "no_verdict"
	}
	if v.At.IsZero() || (c.verdictMaxAge > 0 && time.Since(v.At) > c.verdictMaxAge) {
		return false, "stale"
	}
	if v.Enrolled {
		return true, "enrolled"
	}
	return false, "not_enrolled"
}

func traceInterrupt(req interruptRequest, id events.ResponseEnvelope) {
	record := struct {
		interruptDiagnostic
		SessionID     string `json:"session_id"`
		ResponseID    string `json:"response_id"`
		Callback      int64  `json:"callback_sequence"`
		CallbackKnown bool   `json:"callback_known"`
	}{req.Diagnostic, id.SessionID, id.ResponseID, req.Source.CallbackSequence, req.Source.CallbackKnown}
	// No samples, transcript, speaker name, or enrollment data are included.
	data, err := json.Marshal(record)
	if err == nil {
		log.Printf("obey-voice: barge_in_decision %s", data)
	}
}
