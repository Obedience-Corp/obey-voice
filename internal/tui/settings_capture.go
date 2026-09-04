package tui

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
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
		if err := capture.Start(context.Background()); err != nil {
			return nil, nil, fmt.Errorf("open microphone: %w", err)
		}
		return capture, capture.Stop, nil
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
