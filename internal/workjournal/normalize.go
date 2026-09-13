package workjournal

import (
	"encoding/json"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type Normalizer struct {
	cfg     map[string]any
	secrets []string
}

// Filter high-rate audio/token events before taking any coordinator snapshot.
func diagnosticEvent(e events.Event) bool {
	switch e.(type) {
	case events.WorkStateChanged, events.WorkDelivery, events.RouteDecided,
		events.ToolCallStarted, events.ToolCallFinished, events.WorkerUsageRecorded,
		events.WorkerLimitReached, events.WorkerChargeState, events.SpeechDiagnostic,
		events.JobQueued, events.JobStarted, events.JobFinished, events.JobFailed, events.JobAborted:
		return true
	default:
		return false
	}
}

func NewNormalizer(cfg *config.Config) *Normalizer {
	n := &Normalizer{cfg: map[string]any{}}
	if cfg == nil {
		return n
	}
	if cfg.TTSWorkerToken != "" {
		n.secrets = append(n.secrets, cfg.TTSWorkerToken)
	}
	// Explicit safe scalar allowlist; endpoint, account, tokens and full plans are excluded.
	n.cfg["legacy_worker_provider"] = config.VoiceWorkerProviderOf(cfg)
	n.cfg["legacy_worker_model"] = config.BrainModelFor(config.WorkerBrainConfig(cfg), config.VoiceWorkerProviderOf(cfg))
	n.cfg["persona"] = cfg.ActivePersona
	n.cfg["identity_source"] = "configured"
	n.cfg["worker_mode"] = "legacy"
	n.cfg["voice_tools_enabled"] = cfg.VoiceToolsEnabled
	n.cfg["live_provider"] = cfg.BrainProvider
	n.cfg["live_model"] = config.BrainModelFor(cfg, cfg.BrainProvider)
	n.cfg["tts_provider"] = cfg.TTSProvider
	n.cfg["stt_provider"] = cfg.STTProvider
	n.cfg["build_revision"] = nil
	n.cfg["build_dirty"] = nil
	if bi, ok := debug.ReadBuildInfo(); ok {
		n.cfg["build_path"] = bi.Path
		n.cfg["build_version"] = bi.Main.Version
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				n.cfg["build_revision"] = setting.Value
			case "vcs.modified":
				n.cfg["build_dirty"] = setting.Value == "true"
			}
		}
	}
	if cfg.VoiceWorkers != nil {
		n.cfg["worker_mode"] = cfg.VoiceWorkers.Mode
		n.cfg["worker_revision"] = cfg.VoiceWorkers.Revision
		n.cfg["charge_policy"] = cfg.VoiceWorkers.Policy.ChargePolicy
		n.cfg["paid_enabled"] = cfg.VoiceWorkers.Policy.PaidEnabled
		n.cfg["timezone"] = cfg.VoiceWorkers.Policy.Timezone
	}
	return n
}
func (n *Normalizer) Config() map[string]any {
	out := map[string]any{}
	for k, v := range n.cfg {
		if value, ok := v.(string); ok {
			out[k] = n.Text(value, 1024)
		} else {
			out[k] = v
		}
	}
	return out
}
func (n *Normalizer) Text(s string, limit int) string {
	for _, secret := range n.secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return redactText(s, limit)
}

// Snapshot copies a bounded projection; canonical state is never modified.
func (n *Normalizer) Snapshot(s work.Snapshot) work.Snapshot {
	out := s
	var active, terminal []work.JobView
	for _, j := range s.Jobs {
		if j.State == work.JobTerminal {
			terminal = append(terminal, j)
		} else {
			active = append(active, j)
		}
	}
	sort.SliceStable(terminal, func(i, j int) bool { return terminal[i].TerminalRevision < terminal[j].TerminalRevision })
	if len(terminal) > 100 {
		terminal = terminal[len(terminal)-100:]
	}
	out.Jobs = append(active, terminal...)
	out.Pending = append([]work.ResultView(nil), s.Pending...)
	out.Communicated = append([]work.ResultView(nil), s.Communicated...)
	if len(out.Communicated) > 100 {
		out.Communicated = out.Communicated[len(out.Communicated)-100:]
	}
	// Copy and allowlist every config map before generic text redaction.
	for i := range out.Jobs {
		out.Jobs[i].Config = n.safeConfig(out.Jobs[i].Config)
		if out.Jobs[i].Outcome != nil {
			o := *out.Jobs[i].Outcome
			o.Config = n.safeConfig(o.Config)
			out.Jobs[i].Outcome = &o
		}
	}
	for _, rows := range [][]work.ResultView{out.Pending, out.Communicated} {
		for i := range rows {
			rows[i].Outcome.Config = n.safeConfig(rows[i].Outcome.Config)
		}
	}
	// A JSON projection deep-copies nested optional delivery receipts as well as
	// slices/maps. All string-bearing fields pass one redaction path, including
	// future additive outcome fields, without touching caller-owned pointers.
	data := n.safePayload(out)
	var copied work.Snapshot
	_ = json.Unmarshal(data, &copied)
	return copied
}
func (n *Normalizer) safeConfig(src map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"provider", "model", "persona", "worker_tier", "worker_revision", "tools"} {
		if v, ok := src[k]; ok {
			out[k] = n.Text(v, 1024)
		}
	}
	return out
}
func (n *Normalizer) safePayload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	var tree any
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.UseNumber()
	if decoder.Decode(&tree) != nil {
		return json.RawMessage(`null`)
	}
	var clean func(any, string) any
	clean = func(v any, key string) any {
		switch x := v.(type) {
		case string:
			limit := 1024
			switch key {
			case "text", "preview", "summary", "message", "error":
				limit = 16 << 10
			}
			return n.Text(x, limit)
		case map[string]any:
			for k, value := range x {
				x[k] = clean(value, k)
			}
			return x
		case []any:
			for i, value := range x {
				x[i] = clean(value, key)
			}
			return x
		default:
			return v
		}
	}
	b, _ = json.Marshal(clean(tree, ""))
	return b
}
func eventScope(s work.WorkScope, snap work.Snapshot, id string) (work.WorkScope, bool) {
	if s.Valid() {
		return s, s == snap.Scope
	}
	if id == "" {
		return work.WorkScope{}, true
	}
	for _, j := range snap.Jobs {
		if j.Key.JobID == id {
			return j.Key.Scope, j.Key.Scope == snap.Scope
		}
	}
	return work.WorkScope{}, false
}

func (n *Normalizer) Normalize(e events.Event, snap work.Snapshot) (Record, bool) {
	if e == nil {
		return Record{}, false
	}
	r := Record{Schema: Schema, Type: events.EventType(e), Scope: "session", SessionID: snap.Scope.SessionID, RuntimeID: snap.Scope.RuntimeEpoch, ConversationEpoch: snap.Scope.ConversationEpoch}
	var data any
	var id string
	var sc work.WorkScope
	switch v := e.(type) {
	case events.WorkStateChanged:
		if !v.Snapshot.Scope.Valid() || v.Snapshot.Scope != snap.Scope {
			return Record{}, false
		}
		r.Type = "work.snapshot"
		r.Scope = "work"
		s := n.Snapshot(v.Snapshot)
		data = map[string]any{"snapshot": s, "omitted_jobs": max(0, len(v.Snapshot.Jobs)-len(s.Jobs)), "omitted_communicated": max(0, len(v.Snapshot.Communicated)-len(s.Communicated)), "projection": "bounded_redacted"}
	case events.WorkDelivery:
		r.Type = "work.delivery"
		r.Scope = "work"
		sc = v.Scope
		r.JobID = ""
		data = map[string]any{"stage": v.Stage, "attempt_id": v.AttemptID, "turn_id": v.TurnID, "capture_epoch": v.CaptureEpoch, "result_ids": append([]string(nil), v.ResultIDs...), "reason": n.Text(v.Reason, 1024), "mode": n.Text(v.Mode, 128), "segments": v.Segments}
	case events.RouteDecided:
		sc = v.Scope
		id = v.JobID
		data = map[string]any{"kind": v.Kind, "tier": v.Tier, "reason_code": v.ReasonCode, "state": v.State, "message": n.Text(v.Message, 1024)}
	case events.ToolCallStarted:
		r.Type, r.Scope = "tool.started", "work"
		sc = v.WorkScope
		id = v.JobID
		r.CallID = v.ID
		r.JobID = id
		data = map[string]any{"name": v.Name, "kind": v.Kind, "summary": n.Text(v.Summary, 1024)}
	case events.ToolCallFinished:
		r.Type, r.Scope = "tool.finished", "work"
		sc = v.WorkScope
		id = v.JobID
		r.CallID = v.ID
		r.JobID = id
		originalBytes := int64(len(v.ResultPreview()))
		if v.Outcome != nil && v.Outcome.OutputBytes > originalBytes {
			originalBytes = v.Outcome.OutputBytes
		}
		r.DetailPayload = &DetailPayload{Text: n.Text(v.ResultPreview(), 64<<10), OriginalBytes: originalBytes}
		d := map[string]any{"name": v.Name, "kind": v.Kind, "preview": n.Text(v.ResultPreview(), 1024), "elapsed_ms": v.ResultElapsed().Milliseconds(), "outcome": v.OutcomeStatus(), "preview_truncated": originalBytes > 1024, "output_bytes": originalBytes}
		if v.Outcome != nil {
			d["outcome_detail"] = map[string]any{"status": v.Outcome.Status, "exit_code": v.Outcome.ExitCode, "http_status": v.Outcome.HTTPStatus, "signal": v.Outcome.Signal, "reason": n.Text(v.Outcome.Reason, 1024), "truncated": v.Outcome.Truncated}
		}
		data = d
	case events.WorkerUsageRecorded:
		sc = v.Scope
		id = v.JobID
		r.JobID = id
		data = map[string]any{"requests": v.Requests, "tool_calls": v.ToolCalls, "retries": v.Retries, "input_tokens": v.InputTokens, "output_tokens": v.OutputTokens, "charge_micro_usd": v.ChargeMicroUSD}
	case events.WorkerLimitReached:
		r.Type, r.Scope = "worker.limit_reached", "work"
		sc = v.Scope
		id = v.JobID
		r.JobID = id
		data = map[string]any{"message": n.Text(v.Message, 16<<10), "termination": n.Text(v.Termination, 1024)}
	case events.WorkerChargeState:
		r.Type, r.Scope = "worker.charge", "work"
		sc = v.Scope
		id = v.JobID
		r.JobID = id
		r.RequestID = v.RequestID
		data = map[string]any{"request_id": v.RequestID, "state": v.State, "charge_cap_available": v.ChargeCapAvailable}
	case events.SpeechDiagnostic:
		r.Scope = "audio"
		data = map[string]any{"symptom": v.Symptom, "cause": v.Cause, "confidence": v.Confidence, "provider": v.Provider, "action_code": v.ActionCode, "message": n.Text(v.Message, 16<<10), "gap_ms": v.Gap.Milliseconds(), "sample_count": v.SampleCount, "audio_seconds": v.AudioSeconds, "exclusions": append([]string(nil), v.Exclusions...)}
	default:
		switch v := e.(type) {
		case events.JobQueued:
			id = v.ID
			r.Type, r.Scope = "job.queued", "work"
			data = map[string]any{"task": n.Text(v.Task, 1024)}
		case events.JobStarted:
			id = v.ID
			r.Type, r.Scope = "job.started", "work"
			data = map[string]any{"task": n.Text(v.Task, 1024)}
		case events.JobFinished:
			id = v.ID
			r.Type, r.Scope = "job.ended", "work"
			data = map[string]any{"task": n.Text(v.Task, 1024), "status": "succeeded", "summary": n.Text(v.Summary, 16<<10), "elapsed_ms": v.Elapsed.Milliseconds()}
		case events.JobFailed:
			id = v.ID
			r.Type, r.Scope = "job.ended", "work"
			data = map[string]any{"task": n.Text(v.Task, 1024), "status": "failed", "error": n.Text(v.Err, 16<<10), "elapsed_ms": v.Elapsed.Milliseconds()}
		case events.JobAborted:
			id = v.ID
			r.Type, r.Scope = "job.ended", "work"
			data = map[string]any{"task": n.Text(v.Task, 1024), "status": "aborted", "reason": n.Text(v.Reason, 1024), "elapsed_ms": v.Elapsed.Milliseconds()}
		default:
			return Record{}, false
		}
	}
	if sc2, ok := eventScope(sc, snap, id); !ok {
		return Record{}, false
	} else if sc2.Valid() {
		r.Scope = "work"
		r.SessionID, r.RuntimeID, r.ConversationEpoch = sc2.SessionID, sc2.RuntimeEpoch, sc2.ConversationEpoch
	}
	r.JobID = id
	r.Data = n.safePayload(data)
	return r, true
}
