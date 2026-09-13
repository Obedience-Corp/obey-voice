package workjournal

import (
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Report reprojects saved records through the export allowlist. On-disk JSON is
// not trusted merely because it declares this schema; unknown additive payloads
// remain unknown rather than exporting arbitrary configuration or detail bodies.
func (n *Normalizer) Report(src Report) Report {
	out := src
	out.Notes = append([]string(nil), src.Notes...)
	for i := range out.Notes {
		out.Notes[i] = n.Text(out.Notes[i], 1024)
	}
	out.Checkpoints = append([]Checkpoint(nil), src.Checkpoints...)
	for i := range out.Checkpoints {
		out.Checkpoints[i].Snapshot = n.Snapshot(src.Checkpoints[i].Snapshot)
		out.Checkpoints[i].Health = cloneHealth(src.Checkpoints[i].Health)
		out.Checkpoints[i].Health.LastError = n.Text(out.Checkpoints[i].Health.LastError, 1024)
		out.Checkpoints[i].Projection = "bounded_redacted"
	}
	out.Records = make([]Record, 0, len(src.Records))
	for _, record := range src.Records {
		out.Records = append(out.Records, n.Record(record))
	}
	var clean Report
	_ = json.Unmarshal(n.safePayload(out), &clean)
	return clean
}

func (n *Normalizer) Record(r Record) Record {
	r.DetailPayload = nil
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(r.Data, &fields)
	keep := map[string]json.RawMessage{}
	keys := []string{}
	switch r.Type {
	case "session.started":
		keys = []string{"legacy_worker_provider", "legacy_worker_model", "persona", "identity_source", "worker_mode", "live_provider", "live_model", "tts_provider", "stt_provider", "voice_tools_enabled", "build_revision", "build_dirty", "build_path", "build_version", "worker_revision", "charge_policy", "paid_enabled", "timezone"}
	case "work.snapshot":
		var snapshot struct {
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if json.Unmarshal(r.Data, &snapshot) == nil && len(snapshot.Snapshot) > 0 {
			var state work.Snapshot
			if json.Unmarshal(snapshot.Snapshot, &state) == nil {
				keep["snapshot"] = mustJSON(n.Snapshot(state))
			}
		}
		keys = []string{"omitted_jobs", "omitted_communicated", "projection"}
	case "work.delivery":
		keys = []string{"stage", "attempt_id", "turn_id", "capture_epoch", "result_ids", "reason", "mode", "segments"}
	case "route.decided":
		keys = []string{"kind", "tier", "reason_code", "state", "message", "task"}
	case "job.queued", "job.started":
		keys = []string{"task"}
	case "job.ended":
		keys = []string{"task", "status", "summary", "error", "reason", "elapsed_ms"}
	case "tool.started":
		keys = []string{"name", "kind", "summary"}
	case "tool.finished":
		keys = []string{"name", "kind", "preview", "elapsed_ms", "outcome", "preview_truncated", "output_bytes"}
		keepNested(fields, keep, "outcome_detail", []string{"status", "exit_code", "http_status", "signal", "reason", "truncated"})
		keepNested(fields, keep, "details", []string{"ref", "original_bytes", "retained_bytes", "truncated", "unavailable"})
	case "worker.usage":
		keys = []string{"requests", "tool_calls", "retries", "input_tokens", "output_tokens", "charge_micro_usd"}
	case "worker.limit_reached":
		keys = []string{"message", "termination"}
	case "worker.charge":
		keys = []string{"request_id", "state", "charge_cap_available"}
	case "speech_diagnostic", "speech.diagnostic":
		keys = []string{"symptom", "cause", "confidence", "provider", "action_code", "message", "gap_ms", "sample_count", "audio_seconds", "exclusions"}
	default:
		keep["unrecognized_payload_omitted"] = json.RawMessage("true")
	}
	for _, key := range keys {
		if value, ok := fields[key]; ok && scalarOrScalarList(value) {
			keep[key] = value
		}
	}
	r.Data = n.safePayload(keep)
	// Metadata is also untrusted when read from disk.
	var clean Record
	_ = json.Unmarshal(n.safePayload(r), &clean)
	return clean
}

func scalarOrScalarList(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		return false
	case []any:
		if len(v) > 1024 {
			return false
		}
		for _, x := range v {
			switch x.(type) {
			case map[string]any, []any:
				return false
			}
		}
	}
	return true
}

func keepNested(src, dst map[string]json.RawMessage, key string, allowed []string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(src[key], &fields) != nil {
		return
	}
	out := map[string]json.RawMessage{}
	for _, name := range allowed {
		if value, ok := fields[name]; ok && scalarOrScalarList(value) {
			out[name] = value
		}
	}
	dst[key] = mustJSON(out)
}
