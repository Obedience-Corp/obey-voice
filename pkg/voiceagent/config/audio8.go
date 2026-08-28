package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Audio8Subdir is where the Audio8 ONNX export lives under models_dir. The
// layout lives here, with every other models_dir shape, because config sits
// below tts in the import graph: doctor, the required-asset set and the
// provider all have to name the same directory.
const Audio8Subdir = "audio8-tts"

// Audio8VoicesSubdir is where registered Audio8 voices live under models_dir.
//
// It is a sibling of the export, not a directory inside it, because the export
// is normally a symlink tree into a HuggingFace snapshot: writing a voice into
// it would fail on a read-only cache, and would be thrown away by the next
// snapshot fetch if it succeeded. Registered voices are also the one thing
// under models_dir obey-voice cannot re-fetch.
const Audio8VoicesSubdir = "audio8-voices"

// Audio8PackagedVoice names the voice that ships inside the export
// (reference_codes.npy plus the manifest's reference_text). The name is
// reserved: registration refuses it and removal never touches it.
const Audio8PackagedVoice = "reference"

// A registered voice is a directory holding these two files: the codec frames
// the model conditions on, and the metadata that says what they are.
const (
	Audio8VoiceCodesFile = "codes.npy"
	Audio8VoiceMetaFile  = "voice.json"
)

// Audio8ProviderName is the tts_provider value for the Audio8 provider.
const Audio8ProviderName = "audio8"

// Audio8SnapshotRepo / Audio8SnapshotRev pin the export the tokenizer and
// benchmarks were verified against; obey-voice never downloads it itself.
const (
	Audio8SnapshotRepo = "Audio8/audio8-TTS-0.1B-ONNX-INT8"
	Audio8SnapshotRev  = "e1c07e8a3725077e3ab80ad8578e5787e8a23c6c"
)

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

// Audio8VoicesDir is the registered-voice store under modelsDir.
func Audio8VoicesDir(modelsDir string) string {
	return filepath.Join(modelsDir, Audio8VoicesSubdir)
}

// Audio8VoicesDirFrom resolves the store for an already-loaded Config, the way
// audio8ModelDir resolves the export.
func Audio8VoicesDirFrom(cfg *Config) string {
	return Audio8VoicesDir(ModelsDirFrom(cfg))
}

// Audio8VoiceDir is one registered voice's directory. The name must have been
// through ValidateAudio8VoiceName; callers that take a name from a user run it
// first so no input can escape the store.
func Audio8VoiceDir(modelsDir, name string) string {
	return filepath.Join(Audio8VoicesDir(modelsDir), name)
}

// audio8VoiceNameRE keeps a voice name to one lowercase path component. It is
// stricter than "not a path traversal" on purpose: the name is also a config
// value, a CLI argument and a picker row, and a name that renders differently
// in any of those is a name a user cannot reliably select.
var audio8VoiceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateAudio8VoiceName reports whether name may become a registered voice.
func ValidateAudio8VoiceName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("voice name is required")
	}
	if !audio8VoiceNameRE.MatchString(trimmed) {
		return fmt.Errorf("invalid voice name %q: use 1-64 characters of a-z, 0-9, dot, dash or underscore, starting with a letter or digit", name)
	}
	if trimmed == Audio8PackagedVoice {
		return fmt.Errorf("voice name %q is reserved for the voice packaged with the export", Audio8PackagedVoice)
	}
	return nil
}

// Audio8RegisteredVoiceNames lists the registered voices under modelsDir, in
// lexical order. A directory without a metadata file is not a voice: a
// half-written registration must not be offered as one.
//
// It reports names only. The codes are a model artifact and are read in the
// tts package, which owns the .npy codec — config owns the layout, as it does
// for every other provider's models_dir shape.
func Audio8RegisteredVoiceNames(modelsDir string) []string {
	if modelsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(Audio8VoicesDir(modelsDir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateAudio8VoiceName(entry.Name()) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(Audio8VoicesDir(modelsDir), entry.Name(), Audio8VoiceMetaFile)); err != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}
