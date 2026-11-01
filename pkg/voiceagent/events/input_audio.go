package events

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"

// InputTranscriptUpdated changes the caption of one existing recorded input.
// It is never a new user turn or an assistant response.
type InputTranscriptUpdated struct {
	Trace        *TraceContext        `json:"trace,omitempty"`
	Envelope     *ResponseEnvelope    `json:"envelope,omitempty"`
	UtteranceID  string               `json:"utterance_id"`
	CaptureEpoch uint64               `json:"capture_epoch"`
	Transcript   utterance.Transcript `json:"transcript"`
}

func (InputTranscriptUpdated) eventType() string { return "input_transcript_updated" }
