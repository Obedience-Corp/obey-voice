package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Follow the launcher error through capture resolution and the Settings status
// line: frontend configuration errors must survive the microphone boundary.
func TestEnrollmentCaptureErrorStatus(t *testing.T) {
	frontendErr := errors.New("WebRTC APM is unavailable in this build")
	deviceErr := errors.New("device unavailable")
	for _, tc := range []struct {
		name      string
		selection string
		build     func(*config.Config) (audio.Frontend, error)
		startErr  error
		want      string
		mic       bool
	}{
		{name: "invalid selection", selection: "invalid", build: voiceagent.NewVoiceFrontend, want: "Registration failed: enrollment voice front-end:"},
		{name: "unavailable frontend", build: func(*config.Config) (audio.Frontend, error) { return nil, frontendErr }, want: "Registration failed: enrollment voice front-end: " + frontendErr.Error()},
		{name: "device failure", selection: config.VoiceFrontendOff, build: voiceagent.NewVoiceFrontend, startErr: deviceErr, want: enrollNoMicMessage, mic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			capture := &enrollmentGraphProbe{order: &order, startErr: tc.startErr}
			_, release, err := openEnrollCapture(func() (speaker.CaptureSource, func(), error) {
				return openEnrollmentCaptureGraph(t.Context(), &config.Config{VoiceFrontend: tc.selection}, capture, tc.build)
			})
			if err == nil || release != nil || errors.Is(err, errNoMicrophone) != tc.mic {
				t.Fatalf("incorrect capture error classification: %v", err)
			}
			if capture.started != tc.mic {
				t.Fatalf("frontend failure opened microphone: %+v", capture)
			}
			m := settingsModel{enroll: enrollSession{active: true}}
			m.finishEnroll(enrollDoneMsg{err: err})
			if !strings.HasPrefix(m.message, tc.want) || m.enroll.active {
				t.Fatalf("wrong registration status: %q", m.message)
			}
		})
	}
}
