package stt

import (
	"slices"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Spec describes the runtime capabilities of one normalized STT provider/mode
// pair. It is a side table keyed by config.NormalizeSTT output; it does not
// change provider construction or the Provider interface.
type Spec struct {
	Provider     string
	Mode         string
	EmitsPartial bool // emits PartialTranscript events mid-utterance
	UsesEndpoint bool // recognizer self-endpoints (policy AllowProviderEnd)
	SupportsEOF  bool // finalizes trailing audio when a finite source ends
	RequiresVAD  bool // construction fails without a VAD
	// SegmentDecode reports whether NewSegmentDecoder can build this provider:
	// it decodes a finite span of audio handed to it, with no capture and no
	// VAD of its own.
	SegmentDecode bool
	// Timing lists the granularities the provider reports for decoded text.
	// Every segment decoder reports TimingUtterance. No shipped provider
	// reports TimingWord: none of them returns word boundaries today, and a
	// capability list must not claim one that a caller would then ask for.
	Timing         []string
	RecommendedUse string
}

// specTable holds one entry per normalized provider/mode pair, derived from
// the provider loops: runOfflineLoop is utterance-final (no partials, no
// provider endpoint), runStreamingLoop emits partials and feeds the
// recognizer's endpoint through the policy's AllowProviderEnd gate.
var specTable = []Spec{
	{
		Provider:       config.STTProviderSherpa,
		Mode:           config.STTModeOffline,
		EmitsPartial:   false,
		UsesEndpoint:   false,
		SupportsEOF:    true,
		RequiresVAD:    true,
		SegmentDecode:  true,
		Timing:         []string{TimingUtterance},
		RecommendedUse: "reliable utterance-final Whisper transcription (previous default)",
	},
	{
		Provider:       config.STTProviderSherpa,
		Mode:           config.STTModeStreaming,
		EmitsPartial:   true,
		UsesEndpoint:   true,
		SupportsEOF:    true,
		RequiresVAD:    true,
		SegmentDecode:  false,
		RecommendedUse: "live partial transcripts with recognizer endpointing",
	},
	{
		Provider:       config.STTProviderParakeet,
		Mode:           config.STTModeOffline,
		EmitsPartial:   false,
		UsesEndpoint:   false,
		SupportsEOF:    true,
		RequiresVAD:    true,
		SegmentDecode:  true,
		Timing:         []string{TimingUtterance},
		RecommendedUse: "default; fast, accurate utterance-final transcription (NVIDIA Parakeet TDT)",
	},
	{
		Provider:       config.STTProviderWhisperCPP,
		Mode:           config.STTModeCLI,
		EmitsPartial:   false,
		UsesEndpoint:   false,
		SupportsEOF:    true,
		RequiresVAD:    true,
		SegmentDecode:  true,
		Timing:         []string{TimingUtterance},
		RecommendedUse: "utterance-final transcription via external whisper.cpp CLI",
	},
	{
		Provider:       config.STTProviderTranscribeCPP,
		Mode:           config.STTModeOffline,
		EmitsPartial:   false,
		UsesEndpoint:   false,
		SupportsEOF:    true,
		RequiresVAD:    true,
		SegmentDecode:  true,
		Timing:         []string{TimingUtterance},
		RecommendedUse: "optional; utterance-final Parakeet TDT on GPU via the external transcribe.cpp CLI",
	},
}

// SpecForNormalized returns the capability spec for a normalized provider/mode
// pair. ok is false when no implemented provider matches.
func SpecForNormalized(norm config.NormalizedSTT) (Spec, bool) {
	for _, spec := range specTable {
		if spec.Provider == norm.Provider && spec.Mode == norm.Mode {
			// Copy the slice so a caller cannot edit the table through it.
			spec.Timing = slices.Clone(spec.Timing)
			return spec, true
		}
	}
	return Spec{}, false
}
