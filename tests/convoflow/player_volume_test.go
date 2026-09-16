package main

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestResponseVolumeReachesPhysicalEngineOnly(t *testing.T) {
	for _, gain := range []float64{1, 0, 0.37} {
		response := playback.NewPlayer()
		fixture := playback.NewPlayer()
		recorder := newRecordingPlayer(response)
		// Construction and volume changes do not open a native device.
		configureResponseVolume(recorder, &config.Config{PlaybackVolume: gain})
		if response.Volume() != gain {
			t.Errorf("physical response volume=%v want=%v", response.Volume(), gain)
		}
		if recorder.real != response || fixture.Volume() != 1 {
			t.Error("response configuration replaced the sink or changed fixture gain")
		}
		if response.NegotiatedDevice().SampleRate != 0 || fixture.NegotiatedDevice().SampleRate != 0 {
			t.Error("volume setup unexpectedly opened a device")
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResponseVolumeAllowsRecordingWithoutPhysicalEngine(t *testing.T) {
	configureResponseVolume(nil, &config.Config{PlaybackVolume: 0.37})
	configureResponseVolume(newRecordingPlayer(nil), &config.Config{PlaybackVolume: 0.37})
}
