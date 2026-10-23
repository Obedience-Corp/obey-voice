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

// Preserve the existing fail-open policy while naming which branch admitted
// the interruption. Unknown-but-healthy is distinct from unavailable or stale.
func (c *interruptController) speakerDecision() (bool, string) {
	if c.speakerVerdict == nil {
		return true, "not_configured"
	}
	v := c.speakerVerdict()
	if !v.Healthy {
		return true, "unhealthy_fail_open"
	}
	if !v.HasVerdict {
		return true, "no_verdict_fail_open"
	}
	if c.verdictMaxAge > 0 && time.Since(v.At) > c.verdictMaxAge {
		return true, "stale_fail_open"
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
