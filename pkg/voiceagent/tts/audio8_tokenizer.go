package tts

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/hfbpe"
)

// audio8TokenizerPath is where the export keeps its text frontend: the same
// <model dir>/tokenizer/tokenizer.json the vendor's PromptBuilder reads.
func audio8TokenizerPath(modelDir string) string {
	return config.Audio8TokenizerPath(modelDir)
}

// NewAudio8Tokenizer loads the checkpoint's byte-level BPE.
//
// It is separate from NewAudio8TTS so the factory can fail on a missing or
// unreadable tokenizer.json before the ~1.6 GB of graphs are touched, which is
// the same fail-fast the nil-tokenizer check gives.
func NewAudio8Tokenizer(modelDir string) (Audio8Tokenizer, error) {
	tok, err := hfbpe.Load(audio8TokenizerPath(modelDir))
	if err != nil {
		return nil, fmt.Errorf("audio8 tokenizer: %w", err)
	}
	return tok, nil
}

// Audio8Installed reports whether the Audio8 export is on disk for cfg. The
// provider is in the catalog whether or not it is downloaded — the same as
// qwen3-tts — so the surfaces that describe a provider use this to say "not
// installed" instead of implying it can speak here. doctor asks config the
// same question about the models dir it was handed.
func Audio8Installed(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return config.Audio8ExportInstalled(audio8ModelDir(cfg))
}
