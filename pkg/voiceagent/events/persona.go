package events

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// PersonaSelection is control state, separate from the identity of any reply
// already queued for display or playback.
type PersonaSelection struct {
	// Directories is an immutable session snapshot. Consumers that need to
	// change it must use Clone, as with binding-derived configuration.
	Directories       *config.ConversationDirectories `json:"directories,omitempty"`
	WorkDir           string                          `json:"work_dir,omitempty"`
	WorkDirSource     string                          `json:"work_dir_source,omitempty"`
	SessionID         string                          `json:"session_id"`
	ConversationEpoch string                          `json:"conversation_epoch"`
	PersonaID         string                          `json:"persona_id"`
	DisplayName       string                          `json:"display_name"`
	PromptHash        string                          `json:"prompt_hash"`
	BindingHash       string                          `json:"binding_hash"`
	Revision          uint64                          `json:"revision"`
	Generation        uint64                          `json:"generation"`
	BrainProvider     string                          `json:"brain_provider"`
	BrainModel        string                          `json:"brain_model"`
	VoiceProvider     string                          `json:"voice_provider"`
	Voice             string                          `json:"voice"`
	AudioMode         string                          `json:"audio_mode,omitempty"`
	AudioControls     string                          `json:"audio_controls,omitempty"`
	AudioTranscript   string                          `json:"audio_transcript,omitempty"`
	AudioRetained     bool                            `json:"audio_retained,omitempty"`
	Reason            string                          `json:"reason"`
	// Hosted is set when the persona's backend sends conversation text off
	// this machine.
	Hosted bool `json:"hosted,omitempty"`
}

func (PersonaSelection) eventType() string { return "persona_selected" }

// ResponseEnvelope is copied when a response is admitted. It follows text,
// speech, and metrics through completion, independently of later selections.
type ResponseEnvelope struct {
	DiscussionID      string `json:"discussion_id,omitempty"`
	SessionID         string `json:"session_id"`
	ConversationEpoch string `json:"conversation_epoch"`
	ResponseID        string `json:"response_id"`
	PersonaID         string `json:"persona_id"`
	DisplayName       string `json:"display_name"`
	PromptHash        string `json:"prompt_hash"`
	BindingHash       string `json:"binding_hash"`
	SelectionRevision uint64 `json:"selection_revision"`
	VoiceProvider     string `json:"voice_provider"`
	Voice             string `json:"voice"`
	BrainProvider     string `json:"brain_provider,omitempty"`
	// Hosted is set when the responding persona's backend sends conversation
	// text off this machine.
	Hosted bool `json:"hosted,omitempty"`
}

// WithResponse carries admitted identity through asynchronous speech stages.
func WithResponse(ctx context.Context, envelope ResponseEnvelope) context.Context {
	return context.WithValue(ctx, responseContextKey{}, envelope)
}

type responseContextKey struct{}

func ResponseFromContext(ctx context.Context) ResponseEnvelope {
	envelope, _ := ctx.Value(responseContextKey{}).(ResponseEnvelope)
	return envelope
}

// Attribute copies response identity onto a supported event. Background work
// control events remain independent of the current speaking persona.
func Attribute(event Event, envelope ResponseEnvelope) Event {
	if envelope.ResponseID == "" {
		return event
	}
	switch e := event.(type) {
	case InputTranscriptUpdated:
		e.Envelope = &envelope
		return e
	case VoiceGenerated:
		e.Envelope = &envelope
		return e
	case TurnInterrupted:
		e.Envelope = &envelope
		return e
	case ThinkingStarted:
		e.Envelope = &envelope
		return e
	case ThinkingComplete:
		e.Envelope = &envelope
		return e
	case TurnMetrics:
		e.Envelope = &envelope
		return e
	case ResponseStreamingStarted:
		e.Envelope = &envelope
		return e
	case ResponseSpeakableStarted:
		e.Envelope = &envelope
		return e
	case ResponseDelta:
		e.Envelope = &envelope
		return e
	case ThinkingDelta:
		e.Envelope = &envelope
		return e
	case SpeechSegmentReady:
		e.Envelope = &envelope
		return e
	case GeneratingVoice:
		e.Envelope = &envelope
		return e
	case SpeakingStarted:
		e.Envelope = &envelope
		return e
	case SpeakingComplete:
		e.Envelope = &envelope
		return e
	case SpeakingInterrupted:
		e.Envelope = &envelope
		return e
	case ResponseReady:
		e.Envelope = &envelope
		return e
	default:
		return event
	}
}

// ResponseIdentity returns a detached copy for wire and transcript adapters.
func ResponseIdentity(event Event) *ResponseEnvelope {
	var envelope *ResponseEnvelope
	switch e := event.(type) {
	case VoiceGenerated:
		envelope = e.Envelope
	case TurnInterrupted:
		envelope = e.Envelope
	case ThinkingStarted:
		envelope = e.Envelope
	case ThinkingComplete:
		envelope = e.Envelope
	case TurnMetrics:
		envelope = e.Envelope
	case ResponseStreamingStarted:
		envelope = e.Envelope
	case ResponseSpeakableStarted:
		envelope = e.Envelope
	case ResponseDelta:
		envelope = e.Envelope
	case ThinkingDelta:
		envelope = e.Envelope
	case SpeechSegmentReady:
		envelope = e.Envelope
	case GeneratingVoice:
		envelope = e.Envelope
	case SpeakingStarted:
		envelope = e.Envelope
	case SpeakingComplete:
		envelope = e.Envelope
	case SpeakingInterrupted:
		envelope = e.Envelope
	case ResponseReady:
		envelope = e.Envelope
	}
	if envelope == nil {
		return nil
	}
	copied := *envelope
	return &copied
}
