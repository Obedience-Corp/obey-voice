package tui

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// settingsCaptureFor gives voice registration a microphone.
//
// A running conversation lends its own capture graph: enrollment embeddings
// must come off the same device and front-end live analysis verifies against,
// and the device is already claimed — a second pipeline on it would fail or
// steal frames. Release is then a no-op, because the conversation still owns
// the capture. From the launcher there is no graph, so one is opened for the
// recording and stopped with it.
//
// Entering Settings force-mutes the conversation, but mute pauses dispatch,
// not capture: frames keep flowing, which is exactly why the overlay says the
// microphone is live.
func (a App) settingsCaptureFor() func() (speaker.CaptureSource, func(), error) {
	runtime, cfg := a.runtime, a.cfg
	return func() (speaker.CaptureSource, func(), error) {
		if runtime != nil && runtime.Pipeline != nil && runtime.Pipeline.Capture != nil {
			return runtime.Pipeline.Capture, func() {}, nil
		}
		device := ""
		if cfg != nil {
			device = cfg.InputDevice
		}
		capture := audio.NewCaptureWithDevice(device)
		// Settings only exists inside Run, which has redirected file
		// descriptor 2 into the diagnostics log for the life of the program.
		capture.Logf = log.Printf
		return openEnrollmentCaptureGraph(context.Background(), cfg, capture, voiceagent.NewVoiceFrontend)
	}
}

// settingsLiveSeeder exposes the conversation's live speaker controller so a
// registration can reach the engine that is labelling turns right now.
func (a App) settingsLiveSeeder() func() LiveSpeakerController {
	runtime := a.runtime
	return func() LiveSpeakerController {
		if runtime == nil {
			return nil
		}
		return runtime.LiveSpeaker
	}
}

// enrollmentCaptureDevice keeps launcher enrollment testable without opening
// hardware. A running conversation bypasses this entirely and lends its graph.
type enrollmentCaptureDevice interface {
	speaker.CaptureSource
	SetFrontend(audio.Frontend)
	Start(context.Context) error
	Stop()
}

func openEnrollmentCaptureGraph(ctx context.Context, cfg *config.Config, capture enrollmentCaptureDevice, build func(*config.Config) (audio.Frontend, error)) (speaker.CaptureSource, func(), error) {
	frontend, err := build(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("enrollment voice front-end: %w", err)
	}
	if frontend != nil {
		capture.SetFrontend(frontend)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			capture.Stop()
			if frontend != nil {
				_ = frontend.Close()
			}
		})
	}
	if err := capture.Start(ctx); err != nil {
		release()
		return nil, nil, fmt.Errorf("%w: open microphone: %w", errNoMicrophone, err)
	}
	return capture, release, nil
}
