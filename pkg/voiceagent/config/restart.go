package config

// restartNotRequired lists the keys proven to take effect without restarting
// the agent. Everything absent from this map is assumed to need a restart:
// serve builds its pipeline once at start (cmd/obey-voice/cmd/serve.go
// buildServePipeline) and the TUI snapshots config at conversation entry
// (internal/tui/tui.go), so "no restart" is a claim that must be earned.
//
// Two traps justify the conservative default: newServeMeetingRouter(cfg) and
// newServeMeetingManager(cfg) both capture cfg at serve start, so meeting.* and
// speaker.meeting.* changes are invisible to a running serve despite looking
// like per-operation settings.
var restartNotRequired = map[string]bool{
	"tts_provider":      true, // applies to subsequent utterances
	"tui_mouse_enabled": true, // applies on next enter/leave of the conversation
	"active_persona":    true, // a new conversation re-resolves the binding
	"user_display_name": true, // NameMap seeds at conversation entry from loaded config
}

// restartVerified marks keys whose restart behaviour has been confirmed on
// hardware rather than inferred. Unverified keys still report
// restart_required=true — the marker only records what is still owed, and is
// never shown to users (an app that says "restart, we think" is worse than one
// that just restarts).
var restartVerified = map[string]bool{
	"tts_provider":           true,
	"tui_mouse_enabled":      true,
	"active_persona":         true,
	"user_display_name":      true, // conversation entry re-seeds NameMap from loaded config
	"voice_frontend_enabled": true, // verified: runtime rebuild required
	// voice_frontend and voice_frontend_ns_level are new (S1): both build the
	// same once-at-startup object as the legacy bool above, but that has not
	// itself been hardware-re-verified for these keys, so they report
	// restart_required=true (the honest default for an uncharacterised key)
	// without claiming restart_verified.
	"barge_in_enabled": true, // verified: runtime rebuild required
	// barge_in_vad_threshold, barge_in_tail_guard_ms, and barge_in_speakers
	// are new (S2/S3): the VAD and the interrupt controller are both built
	// once at startup (voiceagent.New), same as above, and are likewise
	// absent from both maps rather than marked verified.
}

// RestartRequired reports whether changing key needs an agent restart to take
// effect. Unknown keys answer true: the honest default for a key nobody has
// characterised.
func RestartRequired(key string) bool {
	return !restartNotRequired[key]
}

// RestartVerified reports whether RestartRequired(key) is evidence rather than
// inference.
func RestartVerified(key string) bool {
	return restartVerified[key]
}
