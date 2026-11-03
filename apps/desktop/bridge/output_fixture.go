//go:build desktopfixture

package main

import (
	"os"
	"path/filepath"

	engine "github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
)

// The isolated silent conversation exercises the real reset/capture controls
// without submitting PCM or opening an output device. It never uses live config.
func configureOutputFixture(rt *engine.DesktopConversation) {
	if os.Getenv("OBEY_VOICE_OUTPUT_FIXTURE") != "1" {
		return
	}
	if rt.Pipeline.Player != nil || rt.Pipeline.HasTTS() {
		panic("output fixture requires a silent conversation")
	}
	if err := audio.SetDebugAudioDir(filepath.Join(os.Getenv("OBEY_VOICE_TEST_CONFIG"), "output-captures")); err != nil {
		panic(err)
	}
	player := playback.NewPlayer()
	_ = audio.SetDebugAudioDir("")
	rt.Pipeline.Player, rt.Output = player, true
	cleanup := rt.Cleanup
	rt.Cleanup = func() {
		_ = player.Close()
		cleanup()
	}
}
