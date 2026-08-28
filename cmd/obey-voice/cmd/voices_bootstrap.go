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
	// The caller canonicalizes; re-check here because the value below decides
	// which voice actually speaks. Kokoro looks its voice up by exact map key
	// and silently falls back to af_heart on a miss, so a name that is merely
	// close enough produces audio in a different voice than the one named.
	canonical, ok := canonicalKokoroVoice(voice)
	if !ok || canonical != voice {
		return nil, 0, codedError(codeInvalidVoice,
			"--from-kokoro %q is not a Kokoro voice (try `obey-voice voices --provider kokoro`)", voice)
	}

	// A copy: bootstrapping a voice must not leave the agent speaking Kokoro.
	speak := *cfg
	speak.TTSProvider = "kokoro"
	speak.TTSVoice = canonical
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
		return nil, 0, fmt.Errorf("kokoro produced no audio for voice %q", canonical)
	}
	return samples, rate, nil
}

// canonicalKokoroVoice resolves a requested voice to the exact name in Kokoro's
// catalog, matching case-insensitively but returning the catalog's spelling.
//
// The exact spelling is what matters downstream twice over: Kokoro's own lookup
// is an exact map key with a silent af_heart fallback, so "AF_SKY" would speak
// as af_heart; and tts.VoiceInfo keys its language table on the lowercase first
// byte, so an uppercased name reports no locale. A registered voice records
// which voice synthesized it, and that record has to be true.
func canonicalKokoroVoice(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, v := range tts.VoiceNames() {
		if strings.EqualFold(v, name) {
			return v, true
		}
	}
	return "", false
}
