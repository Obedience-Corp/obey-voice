package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Voice front-end backends selectable via voice_frontend. webrtc wraps the
// AEC3 shim (pkg/voiceagent/audio/apmfrontend, apm build tag only); nlms is
// the engine's hand-rolled AEC/NS/AGC chain; off runs raw microphone audio.
// See workflow/design/obey-voice-barge-in (DECISIONS.md D1, D5, D8).
const (
	VoiceFrontendWebRTC = "webrtc"
	VoiceFrontendNLMS   = "nlms"
	VoiceFrontendOff    = "off"
)

// DefaultVoiceFrontendNSLevel is APM's noise-suppression aggressiveness
// (0=low .. 3=very high) when nothing overrides it. 1 (moderate) mirrors the
// library's own conservative default.
const DefaultVoiceFrontendNSLevel = 1

func voiceFrontendEnum() []string {
	return []string{VoiceFrontendWebRTC, VoiceFrontendNLMS, VoiceFrontendOff}
}

// VoiceFrontendOf normalizes cfg's front-end selection. Empty (a config
// written before this key existed, and never migrated for some reason, or a
// nil cfg) is off, matching the pre-migration default.
func VoiceFrontendOf(cfg *Config) string {
	if cfg == nil {
		return VoiceFrontendOff
	}
	front := strings.ToLower(strings.TrimSpace(cfg.VoiceFrontend))
	if front == "" {
		return VoiceFrontendOff
	}
	return front
}

// VoiceFrontendNSLevelOf clamps cfg's configured noise-suppression level to
// the range APM accepts (0-3). The level does not decide which front-end
// runs, so an out-of-range value falls back to the default rather than
// failing the whole selection over it.
func VoiceFrontendNSLevelOf(cfg *Config) int {
	if cfg == nil {
		return DefaultVoiceFrontendNSLevel
	}
	if cfg.VoiceFrontendNSLevel < 0 || cfg.VoiceFrontendNSLevel > 3 {
		return DefaultVoiceFrontendNSLevel
	}
	return cfg.VoiceFrontendNSLevel
}

// ValidateVoiceFrontend rejects a voice_frontend selection this build cannot
// honor. webrtc in a build without the apm tag is a hard error (D5): a
// silent fallback to nlms would fake the very behavior slice 1's gate is
// measuring. Called both at startup (a hard failure) and by Diagnose (as an
// error diagnostic), so the two surfaces can never disagree about why.
func ValidateVoiceFrontend(cfg *Config) error {
	switch front := VoiceFrontendOf(cfg); front {
	case VoiceFrontendWebRTC:
		if !WebRTCFrontendBuildAvailable() {
			return fmt.Errorf(
				"voice_frontend=webrtc requires a build with the apm tag against the webrtc-audio-processing library; this binary was built without it — set voice_frontend to nlms or off, or rebuild with -tags apm (see scripts/build-webrtc-apm.sh)")
		}
	case VoiceFrontendNLMS, VoiceFrontendOff:
	default:
		return fmt.Errorf("unsupported voice_frontend %q; want %s, %s, or %s",
			front, VoiceFrontendWebRTC, VoiceFrontendNLMS, VoiceFrontendOff)
	}
	return nil
}

// resolveVoiceFrontendMigration implements D8: an explicit voice_frontend
// wins outright; otherwise the legacy voice_frontend_enabled bool decides,
// preserving pre-migration behavior (true -> nlms, false/absent -> off).
//
// "Explicit" is checked against the config file and environment directly
// (InConfig / envValueFor) rather than vp.IsSet, which also reports true for
// a key that only has a viper default — and voice_frontend needs one of
// those for `config schema` to advertise it, which would make IsSet always
// true and the migration undetectable.
func resolveVoiceFrontendMigration(vp *viper.Viper, cfg *Config) {
	if vp.InConfig("voice_frontend") {
		return
	}
	if _, _, ok := envValueFor("voice_frontend"); ok {
		return
	}
	if cfg.VoiceFrontendEnabled {
		cfg.VoiceFrontend = VoiceFrontendNLMS
	} else {
		cfg.VoiceFrontend = VoiceFrontendOff
	}
}
