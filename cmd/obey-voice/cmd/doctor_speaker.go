//go:build !integration

package cmd

import (
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func bargeInSpeakersDiagsNative(cfg *config.Config) []config.Diagnostic {
	if config.BargeInSpeakersOf(cfg) != config.BargeInSpeakersEnrolledOnly {
		return nil
	}
	if !speaker.FromAppConfig(cfg).LiveActive() {
		return []config.Diagnostic{{
			Name:        "barge-in-speakers-gate",
			Severity:    config.SeverityWarn,
			Detail:      "barge_in_speakers=enrolled_only but speaker.live is disabled: automatic voice interruptions are blocked until live recognition is enabled",
			Remediation: "enable speaker.enabled and speaker.live.enabled",
		}}
	}
	if !hasEnrolledSpeaker(bargeInSpeakersEnrollmentDir(cfg)) {
		return []config.Diagnostic{{
			Name:        "barge-in-speakers-gate",
			Severity:    config.SeverityWarn,
			Detail:      "barge_in_speakers=enrolled_only but no speaker is enrolled: automatic voice interruptions require a fresh enrolled-speaker match",
			Remediation: "enroll a speaker: obey-voice speaker enroll --name <name> --from-wav clip.wav",
		}}
	}
	return nil
}

func speakersEnrollmentDiagsNative(cfg *config.Config) []config.Diagnostic {
	if !speaker.FromAppConfig(cfg).LiveActive() || hasEnrolledSpeaker(bargeInSpeakersEnrollmentDir(cfg)) {
		return nil
	}
	return []config.Diagnostic{{
		Name:        "speakers-enrollment",
		Severity:    config.SeverityWarn,
		Detail:      "No voice registered — live labels will say speaker-1",
		Remediation: "Settings → Speakers → Register your voice, or: obey-voice speaker enroll --name <name> --from-wav clip.wav",
	}}
}

// hasEnrolledSpeaker is a seam so unit tests can exercise the enrolled-store
// doctor path without writing a real enrollment store on the host.
var hasEnrolledSpeaker = func(dir string) bool {
	store, err := speaker.LoadEnrollment(dir)
	return err == nil && store != nil && len(store.List()) > 0
}

func bargeInSpeakersEnrollmentDir(cfg *config.Config) string {
	return speaker.EnrollmentDir(cfg)
}
