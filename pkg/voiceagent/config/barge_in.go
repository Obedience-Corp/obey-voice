package config

import (
	"time"

	"github.com/spf13/viper"
)

// Barge-in tuning (S2, workflow/design/obey-voice-barge-in). Both values
// gate the same interruptController the barge-in VAD feeds: how confident it
// must be that captured audio is speech, and how long after playback pauses
// it ignores capture chunks so speaker echo trailing off cannot accumulate
// toward a trip. See pkg/voiceagent/audio/vad.go and
// pkg/voiceagent/pipeline/interrupt.go.
const (
	// DefaultBargeInVADThreshold matches the constant NewBargeInVAD used
	// before this became configurable. Stricter than the capture VAD's
	// threshold by default so residual TTS echo through the nlms/off
	// front-ends is unlikely to register as an interruption. Droppable
	// toward ~0.6 once voice_frontend=webrtc's AEC does the echo
	// suppression instead of this threshold (DECISIONS.md S2; the value
	// tuned on the S2 hardware session becomes the webrtc-mode default in
	// S4).
	DefaultBargeInVADThreshold = 0.85
	// WebRTCBargeInVADThreshold is the unset webrtc default. APM already
	// rejects echo-only playback at this confidence; 0.85 erased short
	// startup commands whose processed residual still passed 0.6.
	WebRTCBargeInVADThreshold = 0.6
	MinBargeInVADThreshold    = 0.3
	MaxBargeInVADThreshold    = 0.95

	// DefaultBargeInTailGuardMS matches the constant interrupt.go used
	// before this became configurable.
	DefaultBargeInTailGuardMS = 200
	MinBargeInTailGuardMS     = 200
	MaxBargeInTailGuardMS     = 800
)

// BargeInVADThresholdOf clamps cfg's configured barge-in VAD threshold to the
// range the detector accepts (0.3-0.95). An out-of-range or unset (zero)
// value falls back to the default rather than failing the whole selection
// over it — same posture as VoiceFrontendNSLevelOf.
func BargeInVADThresholdOf(cfg *Config) float64 {
	if cfg == nil {
		return DefaultBargeInVADThreshold
	}
	if cfg.BargeInVADThreshold < MinBargeInVADThreshold || cfg.BargeInVADThreshold > MaxBargeInVADThreshold {
		if VoiceFrontendOf(cfg) == VoiceFrontendWebRTC {
			return WebRTCBargeInVADThreshold
		}
		return DefaultBargeInVADThreshold
	}
	return cfg.BargeInVADThreshold
}

// resolveBargeInVADThresholdMigration applies the webrtc 0.6 default when the
// user did not set barge_in_vad_threshold. Viper's schema default is still
// 0.85 (nlms/off), and IsSet cannot distinguish that from an explicit value.
func resolveBargeInVADThresholdMigration(vp *viper.Viper, cfg *Config) {
	if VoiceFrontendOf(cfg) != VoiceFrontendWebRTC {
		return
	}
	if vp.InConfig("barge_in_vad_threshold") {
		return
	}
	if _, _, ok := envValueFor("barge_in_vad_threshold"); ok {
		return
	}
	cfg.BargeInVADThreshold = WebRTCBargeInVADThreshold
}

// BargeInTailGuardMSOf clamps cfg's configured tail-guard window (ms) to the
// range this build accepts (200-800). Out-of-range or unset falls back to
// the default.
func BargeInTailGuardMSOf(cfg *Config) int {
	if cfg == nil {
		return DefaultBargeInTailGuardMS
	}
	if cfg.BargeInTailGuardMS < MinBargeInTailGuardMS || cfg.BargeInTailGuardMS > MaxBargeInTailGuardMS {
		return DefaultBargeInTailGuardMS
	}
	return cfg.BargeInTailGuardMS
}

// BargeInTailGuardOf is BargeInTailGuardMSOf as a time.Duration, which is
// what the interrupt controller actually consumes.
func BargeInTailGuardOf(cfg *Config) time.Duration {
	return time.Duration(BargeInTailGuardMSOf(cfg)) * time.Millisecond
}
