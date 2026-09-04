package speaker

import (
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// EnrollmentDir resolves the profile store location: the configured directory,
// else <config>/speakers. Single source of the rule for `speaker enroll`,
// doctor, and Settings — three call sites that must agree on where a voice
// registered in one of them is read by the others.
func EnrollmentDir(cfg *config.Config) string {
	if cfg != nil && cfg.Speaker.EnrollmentDir != "" {
		return cfg.Speaker.EnrollmentDir
	}
	return filepath.Join(config.ConfigDir(), "speakers")
}

// FromAppConfig maps config.Speaker into speaker.Config.
// Kept here for a single mapping site; config mirrors fields to avoid
// config→speaker imports. Tests cover default round-trip.
func FromAppConfig(cfg *config.Config) Config {
	if cfg == nil {
		return Config{}.Normalize()
	}
	s := cfg.Speaker
	out := Config{
		Enabled:       s.Enabled,
		Threshold:     s.Threshold,
		EnrollmentDir: s.EnrollmentDir,
		Live: LiveConfig{
			Enabled:   s.Live.Enabled,
			Mode:      s.Live.Mode,
			Threshold: s.Live.Threshold,
			WindowMS:  s.Live.WindowMS,
		},
		Meeting: MeetingConfig{
			Enabled:     s.Meeting.Enabled,
			RecordAudio: s.Meeting.RecordAudio,
			NumSpeakers: s.Meeting.NumSpeakers,
			Live:        s.Meeting.Live,
			Diarizer:    s.Meeting.Diarizer,
		},
		Models: ModelsConfig{
			Embedding:    s.Models.Embedding,
			Segmentation: s.Models.Segmentation,
		},
	}
	return out.Normalize()
}
