package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
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

// Audio8VoiceDir is one registered voice's directory. name must be the value
// CanonicalAudio8VoiceName returned, not the raw user input: building a path
// from an unnormalized name is how a voice ends up registered under one
// spelling and looked up under another.
func Audio8VoiceDir(modelsDir, name string) string {
	return filepath.Join(Audio8VoicesDir(modelsDir), name)
}

// audio8VoiceNameRE keeps a voice name to one lowercase path component. It is
// stricter than "not a path traversal" on purpose: the name is also a config
// value, a CLI argument and a picker row, and a name that renders differently
// in any of those is a name a user cannot reliably select.
var audio8VoiceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// CanonicalAudio8VoiceName validates a voice name and returns the single form
// every surface must use for it.
//
// It returns the name rather than only an error because the two used to
// disagree: validation ran on the trimmed name while callers built paths from
// the raw one, so `voices register " bob "` created a directory called " bob ",
// listed it, and let Settings select it - and then synthesis failed forever,
// because resolveVoice trims the requested name and asked for "bob". Surrounding
// whitespace is stripped here, once, and the caller uses what comes back.
//
// Whitespace is stripped rather than refused because the request side already
// trims: a name that only round-trips when it is trimmed has exactly one usable
// spelling, and that is the one to store. Everything else - case, path
// separators, length - is still refused rather than mangled, since silently
// changing those would register a voice under a name the user did not choose.
func CanonicalAudio8VoiceName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("voice name is required")
	}
	if !audio8VoiceNameRE.MatchString(trimmed) {
		return "", fmt.Errorf("invalid voice name %q: use 1-64 characters of a-z, 0-9, dot, dash or underscore, starting with a letter or digit", name)
	}
	if trimmed == Audio8PackagedVoice {
		return "", fmt.Errorf("voice name %q is reserved for the voice packaged with the export", Audio8PackagedVoice)
	}
	return trimmed, nil
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
		// Only already-canonical directories count. A hand-made " bob " could
		// otherwise be listed and selected but never resolved.
		canonical, err := CanonicalAudio8VoiceName(entry.Name())
		if !entry.IsDir() || err != nil || canonical != entry.Name() {
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

// TTSVoiceFor reports the voice the given provider will speak in, read from the
// one config key that provider reads.
//
// It exists because the same three-way substitution had been written out at
// eight call sites — render's synthesizer, its manifest and its resume key, the
// audiobook form, the launcher badge, the speaker test, the persona stack and
// the Settings row — and four of them were wrong. audio8 turned that from a
// cosmetic mismatch into a hard failure: it resolves the voice it is handed and
// refuses one it does not recognise, so a surface that forwards Kokoro's
// af_heart to it stops the render dead.
//
// audio8 is also the only provider whose *unset* key has a name — an empty
// audio8_voice means the reference packaged in the export — so this substitutes
// it rather than returning empty. kokoro and qwen3-tts return their key as it
// is; their fallbacks belong to them, and applying one here would change resume
// keys that already exist.
func TTSVoiceFor(cfg *Config, provider string) string {
	if cfg == nil {
		return ""
	}
	switch normalizeTTSProviderName(provider) {
	case qwen.ProviderName:
		return strings.TrimSpace(cfg.QwenTTSVoice)
	case Audio8ProviderName:
		if voice := strings.TrimSpace(cfg.Audio8Voice); voice != "" {
			return voice
		}
		return Audio8PackagedVoice
	default:
		return strings.TrimSpace(cfg.TTSVoice)
	}
}

// ConfiguredTTSVoice is TTSVoiceFor without the substitution: the raw value of
// the key, where empty means the user has not chosen one.
//
// The two are not interchangeable. A surface that forwards a voice to a
// provider, or shows which voice will speak, wants TTSVoiceFor - audio8 refuses
// a voice it does not recognise, and "" is not one. A surface that records a
// choice wants this one, because there empty means "inherit", and writing
// "reference" into a persona would pin a voice the user never picked.
func ConfiguredTTSVoice(cfg *Config, provider string) string {
	if cfg == nil {
		return ""
	}
	if normalizeTTSProviderName(provider) == Audio8ProviderName {
		return strings.TrimSpace(cfg.Audio8Voice)
	}
	return TTSVoiceFor(cfg, provider)
}

// TTSVoiceKeyFor names the config key holding the given provider's voice, for
// the surfaces that write through `config set` rather than onto a struct.
func TTSVoiceKeyFor(provider string) string {
	switch normalizeTTSProviderName(provider) {
	case qwen.ProviderName:
		return "qwen_tts_voice"
	case Audio8ProviderName:
		return "audio8_voice"
	default:
		return "tts_voice"
	}
}

// SetTTSVoiceFor writes voice onto the key the given provider reads, leaving
// the other providers' keys untouched: they are different namespaces, and a
// Kokoro voice id means nothing to audio8 or to qwen3-tts.
func SetTTSVoiceFor(cfg *Config, provider, voice string) {
	if cfg == nil {
		return
	}
	switch normalizeTTSProviderName(provider) {
	case qwen.ProviderName:
		cfg.QwenTTSVoice = voice
	case Audio8ProviderName:
		cfg.Audio8Voice = voice
	default:
		cfg.TTSVoice = voice
	}
}

func normalizeTTSProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
