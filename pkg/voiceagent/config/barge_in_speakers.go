package config

import (
	"fmt"
	"strings"
	"time"
)

// Barge-in speaker policy (S3, D7): who is allowed to interrupt playback.
// enrolled_only fuses the live speaker analyzer's most recent window verdict
// into the interrupt controller and requires a fresh enrolled match; anyone never consults
// it. See pkg/voiceagent/pipeline/interrupt.go and
// workflow/design/obey-voice-barge-in DECISIONS.md D7.
const (
	BargeInSpeakersAnyone       = "anyone"
	BargeInSpeakersEnrolledOnly = "enrolled_only"
)

func bargeInSpeakersEnum() []string {
	return []string{BargeInSpeakersAnyone, BargeInSpeakersEnrolledOnly}
}

// bargeInSpeakerVerdictWindowMSDefault mirrors internal/speaker.LiveConfig's
// own window_ms default (1500) without importing that package — internal/
// speaker already depends on this one, so the dependency cannot run the other
// way. Kept as an unexported fallback rather than a public constant: nothing
// outside BargeInSpeakerVerdictMaxAgeOf needs it as a decision, only as a
// default.
const bargeInSpeakerVerdictWindowMSDefault = 1500

// BargeInSpeakersOf normalizes cfg's barge-in speaker policy. Empty (unset)
// is anyone, matching pre-S3 behavior; anything else — including garbage —
// passes through unchanged so ValidateBargeInSpeakers can reject it as a
// hard error rather than the selection silently becoming permissive on a
// typo. Same split as VoiceFrontendOf/ValidateVoiceFrontend.
func BargeInSpeakersOf(cfg *Config) string {
	if cfg == nil {
		return BargeInSpeakersAnyone
	}
	v := strings.ToLower(strings.TrimSpace(cfg.BargeInSpeakers))
	if v == "" {
		return BargeInSpeakersAnyone
	}
	return v
}

// ValidateBargeInSpeakers rejects unrecognized policies rather than silently
// downgrading a misspelled enrolled_only to anyone. Called at startup and doctor.
func ValidateBargeInSpeakers(cfg *Config) error {
	switch v := BargeInSpeakersOf(cfg); v {
	case BargeInSpeakersAnyone, BargeInSpeakersEnrolledOnly:
		return nil
	default:
		return fmt.Errorf("unsupported barge_in_speakers %q; want %s or %s",
			v, BargeInSpeakersAnyone, BargeInSpeakersEnrolledOnly)
	}
}

// BargeInSpeakerVerdictMaxAgeOf is 2x the effective speaker.live window
// (D7's staleness bound): a verdict older than this predates the current
// utterance and the interrupt controller must treat it as no verdict at all
// (block enrolled-only interruption). Mirrors speaker.Config.Normalize()'s window_ms default
// without importing internal/speaker — see
// bargeInSpeakerVerdictWindowMSDefault.
func BargeInSpeakerVerdictMaxAgeOf(cfg *Config) time.Duration {
	windowMS := bargeInSpeakerVerdictWindowMSDefault
	if cfg != nil && cfg.Speaker.Live.WindowMS > 0 {
		windowMS = cfg.Speaker.Live.WindowMS
	}
	return time.Duration(windowMS) * 2 * time.Millisecond
}
