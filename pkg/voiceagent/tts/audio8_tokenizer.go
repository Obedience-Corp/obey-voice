package tts

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/hfbpe"
)

// audio8TokenizerPath is where the export keeps its text frontend: the same
// <model dir>/tokenizer/tokenizer.json the vendor's PromptBuilder reads.
func audio8TokenizerPath(modelDir string) string {
	return filepath.Join(modelDir, "tokenizer", "tokenizer.json")
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

// Audio8Installed reports whether the Audio8 export is on disk. The provider is
// in the catalog whether or not it is downloaded — the same as qwen3-tts — so
// the surfaces that describe a provider use this to say "not installed" instead
// of implying it can speak here.
func Audio8Installed(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	dir := audio8ModelDir(cfg)
	for _, path := range []string{
		audio8TokenizerPath(dir),
		filepath.Join(dir, "runtime_manifest.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
