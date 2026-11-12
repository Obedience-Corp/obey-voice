package pipeline

import (
	"encoding/json"
	"log"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Trace low-rate lifecycle metadata to the host's existing diagnostic sink.
// The TUI redirects stderr to native-diagnostics.log even when experimental
// work journaling is disabled. Do not serialize event structs wholesale: they
// can contain transcripts, prompts, tool output and credentials.
func tracePipelineEvent(event events.Event) {
	record := pipelineTraceRecord(event)
	if record == nil {
		return
	}
	data, err := json.Marshal(record)
	if err == nil {
		log.Printf("obey-voice: turn_trace %s", data)
	}
}

func pipelineTraceRecord(event events.Event) map[string]any {
	// Skip high-rate callbacks and token deltas before allocating a record.
	switch event.(type) {
	case events.STTPhase, events.UserInput, events.ThinkingStarted, events.ThinkingComplete,
		events.SpeakingStarted, events.SpeakingComplete, events.SpeakingInterrupted,
		events.TurnInterrupted, events.ResponseReady, events.TurnMetrics, events.Error:
	default:
		return nil
	}
	r := map[string]any{"event": events.EventType(event)}
	switch e := event.(type) {
	case events.STTPhase:
		r["phase"], r["elapsed_ms"] = e.Phase, e.Elapsed.Milliseconds()
	case events.UserInput:
		r["input_bytes"] = len(e.Text)
	case events.ThinkingStarted:
	case events.ThinkingComplete:
		r["elapsed_ms"] = e.Elapsed.Milliseconds()
	case events.SpeakingStarted:
		r["sink"], r["elapsed_ms"] = e.AudibleSink, e.Elapsed.Milliseconds()
	case events.SpeakingComplete:
		r["sink"], r["interrupted"] = e.AudibleSink, e.Interrupted
		r["elapsed_ms"] = e.Elapsed.Milliseconds()
	case events.SpeakingInterrupted:
		r["reason"] = e.Reason
		r["since_playback_ms"] = e.SincePlaybackStart.Milliseconds()
		r["stop_latency_ms"] = e.DetectionToPlaybackStop.Milliseconds()
	case events.TurnInterrupted:
		r["reason"] = e.Reason
	case events.ResponseReady:
		r["interrupted"] = e.Interrupted
	case events.TurnMetrics:
		r["outcome"], r["interrupted"], r["degraded"] = e.Outcome, e.Interrupted, e.Degraded
		r["sink"], r["tts_provider"] = e.AudibleSink, e.TTSProvider
		r["timing"], r["stall_watchdog"], r["underruns"] = e.Timing, e.StallWatchdogFired, e.Underruns
		if e.SpeechStop != nil {
			r["speech_stop"] = e.SpeechStop
		}
	case events.Error:
		r["stage"] = e.Stage
	default:
		return nil
	}
	if id := events.ResponseIdentity(event); id != nil {
		r["session_id"], r["response_id"] = id.SessionID, id.ResponseID
		r["persona_id"] = id.PersonaID
	}
	return r
}
