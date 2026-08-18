//go:build !integration

package cmd

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func init() {
	newVoiceDeviceChecker = func() config.VoiceDeviceChecker {
		return audio.NewDeviceChecker()
	}
}
