package config

import (
	"os"
	"path/filepath"
)

// Audio8Subdir is where the Audio8 ONNX export lives under models_dir. The
// layout lives here, with every other models_dir shape, because config sits
// below tts in the import graph: doctor, the required-asset set and the
// provider all have to name the same directory.
const Audio8Subdir = "audio8-tts"

// audio8ExportRequired are the files that make an export usable: the text
// frontend the Go BPE reads, and the manifest the provider needs before it can
// open a single graph.
var audio8ExportRequired = []string{
	filepath.Join("tokenizer", "tokenizer.json"),
	"runtime_manifest.json",
}

// Audio8TokenizerPath is the export's tokenizer inside dir — the same
// <export>/tokenizer/tokenizer.json the vendor's own prompt builder reads.
func Audio8TokenizerPath(dir string) string {
	return filepath.Join(dir, "tokenizer", "tokenizer.json")
}

// Audio8ExportInstalled reports whether dir holds an Audio8 export.
//
// obey-voice does not download this export: unlike kokoro and qwen3-tts it is
// not in any manifest, so ManagedTTS excludes it and `models ensure` installs
// nothing for it. Presence on disk is the only signal there is.
func Audio8ExportInstalled(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range audio8ExportRequired {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
