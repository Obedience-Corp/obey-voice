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
	if identity := events.ResponseIdentity(e); identity != nil {
		env["response_envelope"] = identity
	}

	switch e := e.(type) {
	case events.PersonaSelection:
		env["selection"] = e
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
		env["first_token_ms"] = ms(e.FirstTokenElapsed)
		env["first_think_ms"] = ms(e.FirstThinkElapsed)
		env["first_speakable_ms"] = ms(e.FirstSpeakableElapsed)
		env["first_admit_ms"] = ms(e.FirstAdmitElapsed)
		env["synth_first_frame_ms"] = ms(e.SynthFirstFrameElapsed)
		env["ttfa_ms"] = ms(e.TTFAElapsed)
		audible := e.AudibleSink
		if audible == "" {
			audible = "none"
		}
		env["audible_sink"] = audible
		env["think_leak"] = e.ThinkLeak
		// TTS accounting. Additive keys: the milestones above describe only
		// the first segment, and a remote client benchmarking a long reply
		// needs every segment plus the stall budget the turn ran under.
		env["tts_provider"] = e.TTSProvider
		env["tts_tier"] = e.TTSTier
		env["tts_place"] = e.TTSPlace
		env["tts_streaming"] = e.TTSWorkerStreaming
		env["tts_warm"] = e.TTSWarm
		env["tts_fallbacks"] = e.TTSFallbacks
		env["first_audio_grace_ms"] = ms(e.FirstAudioGrace)
		env["stall_watchdog_fired"] = e.StallWatchdogFired
		env["dead_air_ms"] = ms(e.DeadAirElapsed)
		env["underruns"] = e.Underruns
		if len(e.Segments) > 0 {
			segments := make([]map[string]any, 0, len(e.Segments))
			for _, seg := range e.Segments {
				segments = append(segments, map[string]any{
					"i":                       seg.Index,
					"chars":                   seg.Chars,
					"engine_ms":               ms(seg.EngineElapsed),
					"queue_ms":                ms(seg.QueueElapsed()),
					"synth_ms":                ms(seg.SynthElapsed),
					"audio_ms":                int64(seg.AudioSeconds * 1000),
					"rtf":                     seg.RealtimeFactor(),
					"play_ms":                 ms(seg.PlayElapsed),
					"fallback":                seg.Fallback,
					"interrupted":             seg.Interrupted,
					"provider":                seg.Provider,
					"tier":                    seg.Tier,
					"place":                   seg.Place,
					"streaming":               seg.WorkerStreaming,
					"text_ready_ms":           ms(seg.TextReadyElapsed),
					"warm_state":              seg.WarmState,
					"error":                   seg.Error,
					"exclusions":              append([]string(nil), seg.Exclusions...),
					"whole_sentence_playback": seg.WholeSentencePlayback,
					"underruns":               seg.Underruns,
					"gap_ms":                  ms(seg.GapElapsed),
					"gap_cause":               seg.GapCause,
					"gaps":                    encodeSpeechGaps(seg.Gaps),
					"synth_queued_ms":         ms(seg.SynthQueuedElapsed),
					"synth_started_ms":        ms(seg.SynthStartedElapsed),
					"synth_first_pcm_ms":      ms(seg.SynthFirstPCMElapsed),
					"synth_ended_ms":          ms(seg.SynthEndedElapsed),
					"playback_started_ms":     ms(seg.PlaybackStartedElapsed),
					"playback_ended_ms":       ms(seg.PlaybackEndedElapsed),
				})
			}
			env["segments"] = segments
		}
	case events.ResponseStreamingStarted:
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.ResponseSpeakableStarted:
		env["elapsed_ms"] = ms(e.Elapsed)
	case events.ResponseDelta:
		// One incremental piece of assistant text. Named "text" because that
		// is the field every other text-bearing event on this wire uses, and
		// what existing clients already decode.
		env["text"] = e.Text
	case events.ThinkingDelta:
		env["text"] = e.Text
	case events.SpeechSegmentReady:
		env["text"] = e.Text
		if e.Elapsed > 0 {
			env["elapsed_ms"] = ms(e.Elapsed)
		}
	case events.GeneratingVoice:
		env["sentence"] = e.Sentence
	case events.VoiceGenerated:
		env["sentence"] = e.Sentence
		env["elapsed_ms"] = ms(e.Elapsed)
		env["index"] = e.Index
		env["fallback"] = e.Fallback
	case events.SpeakingStarted:
		env["text"] = e.Text
		if e.AudibleSink != "" {
			env["audible_sink"] = e.AudibleSink
		}
		if e.Elapsed > 0 {
			env["elapsed_ms"] = ms(e.Elapsed)
		}
	case events.SpeakingComplete:
		env["elapsed_ms"] = ms(e.Elapsed)
		env["interrupted"] = e.Interrupted
		if e.AudibleSink != "" {
			env["audible_sink"] = e.AudibleSink
		}
	case events.SpeakingInterrupted:
		env["reason"] = e.Reason
		env["since_playback_start_ms"] = ms(e.SincePlaybackStart)
	case events.TurnInterrupted:
		env["reason"] = e.Reason
		env["since_playback_start_ms"] = ms(e.SincePlaybackStart)
	case events.ResponseReady:
		env["response"] = e.Response
		env["interrupted"] = e.Interrupted
	case events.ToolCallStarted:
		env["id"] = e.ID
		env["job_id"] = e.JobID
		env["name"] = e.Name
		env["kind"] = e.Kind
		env["summary"] = e.Summary
		if e.WorkScope.Valid() {
			env["work_scope"] = e.WorkScope
		}
	case events.ToolCallFinished:
		env["id"] = e.ID
		env["job_id"] = e.JobID
		env["name"] = e.Name
		env["kind"] = e.Kind
		env["preview"] = e.ResultPreview()
		env["message"] = e.ResultReason()
		env["elapsed_ms"] = ms(e.ResultElapsed())
		env["status"] = string(e.OutcomeStatus())
		if e.WorkScope.Valid() {
			env["work_scope"] = e.WorkScope
		}
		env["exit_code"] = nil
		env["http_status"] = nil
		if o := e.Outcome; o != nil {
			env["exit_code"] = o.ExitCode
			env["http_status"] = o.HTTPStatus
			env["signal"] = o.Signal
			env["reason"] = o.Reason
			env["input_summary"] = o.InputSummary
			env["output_ref"] = o.OutputRef
			env["output_bytes"] = o.OutputBytes
			env["truncated"] = o.Truncated
		}
	case events.Error:
		env["stage"] = e.Stage
		env["message"] = e.Message
	case events.Info:
		env["message"] = e.Message
	case events.WorkStateChanged:
		env["work"] = e.Snapshot
	case events.WorkDelivery:
		env["delivery"] = e
	case events.SpeechDiagnostic:
		env["symptom"] = e.Symptom
		env["cause"] = e.Cause
		env["confidence"] = e.Confidence
		env["provider"] = e.Provider
		env["scope"] = e.Scope
		env["action_code"] = e.ActionCode
		env["dedupe_key"] = e.DedupeKey
		env["message"] = e.Message
		env["engine_rtf"] = e.EngineRTF
		env["gap_ms"] = ms(e.Gap)
		env["sample_count"] = e.SampleCount
		env["audio_seconds"] = e.AudioSeconds
		if len(e.Exclusions) > 0 {
			env["exclusions"] = append([]string(nil), e.Exclusions...)
		}
	}
	// ThinkingStarted and ConversationCleared carry no fields.

	return env
}

func encodeSpeechGaps(in []events.SpeechGap) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, g := range in {
		out = append(out, map[string]any{
			"start_ms": ms(g.StartElapsed),
			"end_ms":   ms(g.EndElapsed),
			"cause":    g.Cause,
		})
	}
	return out
}

func marshalEvent(e events.Event) ([]byte, error) {
	return json.Marshal(encodeEvent(e))
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// controlMessage is one client -> server message on /v1/stream.
type controlMessage struct {
	ID         string `json:"id,omitempty"` // transcribe_start: client request identity
	Type       string `json:"type"`         // text_input | interrupt | clear_history | audio_output | voice_start | voice_end | audio_input | set_persona
	Text       string `json:"text,omitempty"`
	Mode       string `json:"mode,omitempty"`   // audio_output: "stream" | "local" | "off"
	Binary     bool   `json:"binary,omitempty"` // audio_output: TTS chunks as OVA1 binary frames
	Data       string `json:"data,omitempty"`   // audio_input: base64 pcm_s16le mono @ 16 kHz
	Name       string `json:"name,omitempty"`   // set_persona: persona id
	SampleRate int    `json:"sample_rate,omitempty"`
}
