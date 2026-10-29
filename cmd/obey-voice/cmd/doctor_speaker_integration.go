//go:build integration

package cmd

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"

// The integration binary is deliberately CGO-free. Keep doctor useful for
// configuration checks while leaving native speaker enrollment/model checks to
// the host build, where the sherpa audio dependency is available.
func bargeInSpeakersDiagsNative(cfg *config.Config) []config.Diagnostic {
	if config.BargeInSpeakersOf(cfg) != config.BargeInSpeakersEnrolledOnly {
		return nil
	}
	if cfg == nil || !cfg.Speaker.Enabled || !cfg.Speaker.Live.Enabled {
		return []config.Diagnostic{{
			Name:        "barge-in-speakers-gate",
			Severity:    config.SeverityWarn,
			Detail:      "barge_in_speakers=enrolled_only but speaker.live is disabled: automatic voice interruptions are blocked until live recognition is enabled",
			Remediation: "enable speaker.enabled and speaker.live.enabled",
		}}
	}
	return nil
}

func speakersEnrollmentDiagsNative(*config.Config) []config.Diagnostic {
	return nil
}
