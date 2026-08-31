//go:build !apm

package config

// WebRTCFrontendBuildAvailable is false without the apm tag: the
// webrtc-audio-processing shim (pkg/voiceagent/audio/apm,
// pkg/voiceagent/audio/apmfrontend's real implementation, cmd/apmharness) is
// entirely excluded from this build, so voice_frontend: webrtc cannot
// construct anything. See ValidateVoiceFrontend and DECISIONS.md D5 — this
// must produce a clear rejection, never a silent fallback to nlms.
func WebRTCFrontendBuildAvailable() bool { return false }
