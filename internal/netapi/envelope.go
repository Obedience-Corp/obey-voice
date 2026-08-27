package netapi

import (
	"encoding/json"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// encodeEvent maps a bus event to its wire envelope: the eventType wire name
// as a "type" discriminator plus explicit lowercase fields. Durations are
// encoded as integer milliseconds — Go's default for time.Duration is
// nanoseconds, which no client wants.
func encodeEvent(e events.Event) map[string]any {
	env := map[string]any{"type": events.EventType(e)}

	switch e := e.(type) {
	case events.STTPhase:
		env["phase"] = e.Phase
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.UserInput:
		env["text"] = e.Text
		if e.Speaker != "" {
			env["speaker"] = e.Speaker
		}
	case events.TranscriptPartial:
		env["text"] = e.Text
	case events.AudioLevel:
		// "level" is the normalised RMS energy in [0, 1] the engine already
		// computes for its own meters; "source" is "input" (mic) or "output".
		env["source"] = e.Source
		env["level"] = e.Level
	case events.ThinkingComplete:
		env["response"] = e.Response
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.TurnMetrics:
		env["outcome"] = e.Outcome
		env["interrupted"] = e.Interrupted
		env["stt_final_ms"] = ms(e.STTFinalElapsed)
		env["first_model_chunk_ms"] = ms(e.FirstModelChunkElapsed)
		env["model_complete_ms"] = ms(e.ModelCompleteElapsed)
		env["first_segment_ms"] = ms(e.FirstSegmentElapsed)
		env["first_audio_ready_ms"] = ms(e.FirstAudioReadyElapsed)
		env["playback_start_ms"] = ms(e.PlaybackStartElapsed)
		env["playback_complete_ms"] = ms(e.PlaybackCompleteElapsed)
		env["barge_in_ms"] = ms(e.BargeInElapsed)
		// TTS accounting. Additive keys: the milestones above describe only
		// the first segment, and a remote client benchmarking a long reply
		// needs every segment plus the stall budget the turn ran under.
		env["tts_provider"] = e.TTSProvider
		env["tts_tier"] = e.TTSTier
		env["tts_fallbacks"] = e.TTSFallbacks
		env["first_audio_grace_ms"] = ms(e.FirstAudioGrace)
		env["stall_watchdog_fired"] = e.StallWatchdogFired
		env["dead_air_ms"] = ms(e.DeadAirElapsed)
		if len(e.Segments) > 0 {
			segments := make([]map[string]any, 0, len(e.Segments))
			for _, seg := range e.Segments {
				segments = append(segments, map[string]any{
					"i":           seg.Index,
					"chars":       seg.Chars,
					"engine_ms":   ms(seg.EngineElapsed),
					"queue_ms":    ms(seg.QueueElapsed()),
					"synth_ms":    ms(seg.SynthElapsed),
					"audio_ms":    int64(seg.AudioSeconds * 1000),
					"rtf":         seg.RealtimeFactor(),
					"play_ms":     ms(seg.PlayElapsed),
					"fallback":    seg.Fallback,
					"interrupted": seg.Interrupted,
				})
			}
			env["segments"] = segments
		}
	case events.ResponseStreamingStarted:
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.ResponseDelta:
		// One incremental piece of assistant text. Named "text" because that
		// is the field every other text-bearing event on this wire uses, and
		// what existing clients already decode.
		env["text"] = e.Text
	case events.SpeechSegmentReady:
		env["text"] = e.Text
	case events.GeneratingVoice:
		env["sentence"] = e.Sentence
	case events.VoiceGenerated:
		env["sentence"] = e.Sentence
		env["elapsed_ms"] = ms(e.Elapsed)
		env["index"] = e.Index
		env["fallback"] = e.Fallback
	case events.SpeakingStarted:
		env["text"] = e.Text
	case events.SpeakingComplete:
		env["elapsed_ms"] = ms(e.Elapsed)
		env["interrupted"] = e.Interrupted
	case events.SpeakingInterrupted:
		env["reason"] = e.Reason
	case events.TurnInterrupted:
		env["reason"] = e.Reason
	case events.ResponseReady:
		env["response"] = e.Response
		env["interrupted"] = e.Interrupted
	case events.Error:
		env["stage"] = e.Stage
		env["message"] = e.Message
	case events.Info:
		env["message"] = e.Message
	}
	// ThinkingStarted and ConversationCleared carry no fields.

	return env
}

func marshalEvent(e events.Event) ([]byte, error) {
	return json.Marshal(encodeEvent(e))
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// controlMessage is one client -> server message on /v1/stream.
type controlMessage struct {
	Type       string `json:"type"` // text_input | interrupt | clear_history | audio_output | voice_start | voice_end | audio_input | set_persona
	Text       string `json:"text,omitempty"`
	Mode       string `json:"mode,omitempty"` // audio_output: "stream" | "local" | "off"
	Data       string `json:"data,omitempty"` // audio_input: base64 pcm_s16le mono @ 16 kHz
	Name       string `json:"name,omitempty"` // set_persona: persona id
	SampleRate int    `json:"sample_rate,omitempty"`
}
