package tui

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type enrollmentGraphProbe struct {
	frontend audio.Frontend
	started  bool
	startErr error
	order    *[]string
}

func (c *enrollmentGraphProbe) Subscribe(int) (int, <-chan []float32) { return 0, nil }
func (c *enrollmentGraphProbe) Unsubscribe(int)                       {}
func (c *enrollmentGraphProbe) SetFrontend(f audio.Frontend) {
	if c.started {
		panic("frontend installed after capture started")
	}
	c.frontend = f
}
func (c *enrollmentGraphProbe) Start(context.Context) error {
	c.started = true
	*c.order = append(*c.order, "start")
	return c.startErr
}
func (c *enrollmentGraphProbe) Stop() { *c.order = append(*c.order, "stop") }

type enrollmentFrontendProbe struct{ order *[]string }

func (*enrollmentFrontendProbe) ProcessCapture(in []float32) []float32 { return in }
func (*enrollmentFrontendProbe) PushPlaybackReference([]float32)       {}
func (f *enrollmentFrontendProbe) Close() error {
	*f.order = append(*f.order, "close frontend")
	return nil
}

func TestLauncherEnrollmentUsesConfiguredCaptureProcessing(t *testing.T) {
	for _, selection := range []string{config.VoiceFrontendNLMS, config.VoiceFrontendOff} {
		t.Run(selection, func(t *testing.T) {
			var order []string
			capture := &enrollmentGraphProbe{order: &order}
			cfg := &config.Config{VoiceFrontend: selection}
			got, release, err := openEnrollmentCaptureGraph(t.Context(), cfg, capture, voiceagent.NewVoiceFrontend)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if got != capture || !capture.started || (capture.frontend != nil) != (selection != config.VoiceFrontendOff) {
				t.Fatalf("enrollment bypassed configured processing: %+v", capture)
			}
		})
	}
}

func TestEnrollmentCaptureReleaseOrderAndStartupFailure(t *testing.T) {
	for _, startErr := range []error{nil, errors.New("device unavailable")} {
		var order []string
		capture := &enrollmentGraphProbe{order: &order, startErr: startErr}
		build := func(*config.Config) (audio.Frontend, error) { return &enrollmentFrontendProbe{order: &order}, nil }
		_, release, err := openEnrollmentCaptureGraph(t.Context(), nil, capture, build)
		if startErr == nil {
			if err != nil {
				t.Fatal(err)
			}
			release()
			release()
		} else if !errors.Is(err, startErr) || release != nil {
			t.Fatalf("failed startup lost error or ownership: %v", err)
		}
		if !reflect.DeepEqual(order, []string{"start", "stop", "close frontend"}) {
			t.Fatalf("unsafe or duplicate resource cleanup: %v", order)
		}
	}
}

func TestEnrollmentRejectsUnsupportedFrontendBeforeOpeningMic(t *testing.T) {
	var order []string
	capture := &enrollmentGraphProbe{order: &order}
	_, release, err := openEnrollmentCaptureGraph(t.Context(), &config.Config{VoiceFrontend: "invalid"}, capture, voiceagent.NewVoiceFrontend)
	if err == nil || release != nil || capture.started {
		t.Fatalf("invalid frontend silently became raw capture: err=%v started=%v", err, capture.started)
	}
}
