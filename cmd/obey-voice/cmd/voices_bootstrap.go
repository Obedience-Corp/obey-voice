//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// synthesizeBootstrapAudio speaks the reference line with Kokoro, in process,
// and returns the PCM.
//
// This is the whole of the "no recording needed" path: an English voice that
// clones nobody, because the only thing the encoder ever hears is audio this
// computer just made. Kokoro is used rather than the configured provider
// because it is the one engine obey-voice installs by default and the one whose
// voices are known at compile time.
func synthesizeBootstrapAudio(ctx context.Context, cfg *config.Config, voice, text string) ([]float32, int, error) {
	voice = strings.TrimSpace(voice)
	if _, err := tts.StaticVoices("kokoro", "", ""); err != nil {
		return nil, 0, err
	}
	if !kokoroHasVoice(voice) {
		return nil, 0, codedError(codeInvalidVoice,
			"--from-kokoro %q is not a Kokoro voice (try `obey-voice voices --provider kokoro`)", voice)
	}

	// A copy: bootstrapping a voice must not leave the agent speaking Kokoro.
	speak := *cfg
	speak.TTSProvider = "kokoro"
	speak.TTSVoice = voice
	if err := requireTTSAssets(&speak); err != nil {
		return nil, 0, err
	}
	provider, cleanup, err := voiceStack.newProvider(&speak)
	if err != nil {
		return nil, 0, codedError(codeInvalidProvider, "init Kokoro for the bootstrap: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	stream, err := provider.Synthesize(ctx, text)
	if err != nil {
		return nil, 0, fmt.Errorf("synthesizing the reference line: %w", err)
	}
	samples, rate, err := drainPCMStream(ctx, stream, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("reading the reference line: %w", err)
	}
	if len(samples) == 0 || rate <= 0 {
		return nil, 0, fmt.Errorf("kokoro produced no audio for voice %q", voice)
	}
	return samples, rate, nil
}

func kokoroHasVoice(name string) bool {
	if name == "" {
		return false
	}
	for _, v := range tts.VoiceNames() {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}
