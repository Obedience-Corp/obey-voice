package tts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Provenance kinds recorded in voice.json. They exist so a listing can always
// answer "where did this voice come from" - the difference between a recording
// of a person and audio this machine synthesized is the difference that matters
// when someone asks whether a voice was cloned.
const (
	Audio8VoiceRecorded  = "recording"
	Audio8VoiceSynthetic = "synthetic"
)

// ErrAudio8VoiceNotFound is returned when a name has no registered voice.
var ErrAudio8VoiceNotFound = errors.New("audio8: no such registered voice")

// Audio8VoiceProvenance records where a voice's reference audio came from.
type Audio8VoiceProvenance struct {
	// Kind is Audio8VoiceRecorded or Audio8VoiceSynthetic.
	Kind string `json:"kind"`
	// SourceFile is the base name of the WAV that was registered, never its
	// path: the path is the user's filesystem and this file is meant to be
	// copyable between machines.
	SourceFile       string  `json:"source_file,omitempty"`
	SourceSHA256     string  `json:"source_sha256,omitempty"`
	SourceSampleRate int     `json:"source_sample_rate,omitempty"`
	SourceSeconds    float64 `json:"source_seconds,omitempty"`
	// BootstrapProvider/BootstrapVoice name the engine that produced synthetic
	// reference audio, so a synthesized voice can never be mistaken for a
	// recording of a person.
	BootstrapProvider string `json:"bootstrap_provider,omitempty"`
	BootstrapVoice    string `json:"bootstrap_voice,omitempty"`
	// Consent records that the person registering a recording affirmed they had
	// permission to use that voice. It is always false for synthetic audio,
	// where there is no one to ask.
	//
	// Deliberately not omitempty: this is the field someone reads to answer
	// "was this cloned from a person, and were they asked". An absent key would
	// be indistinguishable from an explicit false, and from a voice.json
	// written before the field existed.
	Consent bool `json:"consent"`
}

// Audio8VoiceMeta is voice.json: everything about a registered voice except the
// codes themselves.
type Audio8VoiceMeta struct {
	Name             string                `json:"name"`
	Transcript       string                `json:"transcript"`
	Locale           string                `json:"locale,omitempty"`
	ModelFingerprint string                `json:"model_fingerprint"`
	NumCodebooks     int                   `json:"num_codebooks"`
	Frames           int                   `json:"frames"`
	SampleRate       int                   `json:"sample_rate"`
	CreatedAt        time.Time             `json:"created_at"`
	Provenance       Audio8VoiceProvenance `json:"provenance"`
}

// Synthetic reports whether the reference audio was synthesized rather than
// recorded from a person.
func (m Audio8VoiceMeta) Synthetic() bool { return m.Provenance.Kind == Audio8VoiceSynthetic }

// Seconds is the reference audio's duration as the model sees it.
func (m Audio8VoiceMeta) Seconds() float64 {
	if m.Frames <= 0 {
		return 0
	}
	return float64(m.Frames) / audio8CodecFrameRateHz
}

// audio8CodecFrameRateHz is 44100 / 2048, the codec's frame rate. Recorded in
// docs/benchmarks/A1-audio8-tts-onnx-2026-08-27.json as codec_frame_rate_hz.
const audio8CodecFrameRateHz = 21.533203125

// SaveAudio8Voice writes one registered voice into the store under modelsDir.
//
// The write is atomic at the voice level: codes and metadata land in a
// temporary directory that is renamed into place, so a crash mid-registration
// leaves either the previous voice or nothing - never a voice whose transcript
// and codes disagree.
func SaveAudio8Voice(modelsDir string, meta Audio8VoiceMeta, codes [][]int64) error {
	name, err := config.CanonicalAudio8VoiceName(meta.Name)
	if err != nil {
		return err
	}
	// The stored record and the directory both take the canonical name, so a
	// voice can never be filed under a spelling it cannot be asked for by.
	meta.Name = name
	// Collapse the transcript here rather than at each caller: it is prepended
	// to every prompt, so stray line breaks from a copy-pasted script would be
	// tokenized on every turn for the life of the voice.
	meta.Transcript = strings.Join(strings.Fields(meta.Transcript), " ")
	if meta.Transcript == "" {
		return fmt.Errorf("audio8: voice %q needs the transcript of its reference audio", meta.Name)
	}
	if strings.TrimSpace(meta.ModelFingerprint) == "" {
		return fmt.Errorf("audio8: voice %q needs the model fingerprint its codes belong to", meta.Name)
	}
	if len(codes) == 0 || len(codes[0]) == 0 {
		return fmt.Errorf("audio8: voice %q has no codes", meta.Name)
	}
	meta.NumCodebooks = len(codes)
	meta.Frames = len(codes[0])
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}

	root := config.Audio8VoicesDir(modelsDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("audio8: create voice store %s: %w", root, err)
	}
	staging, err := os.MkdirTemp(root, "."+meta.Name+".")
	if err != nil {
		return fmt.Errorf("audio8: stage voice %q: %w", meta.Name, err)
	}
	defer os.RemoveAll(staging)

	if err := writeNPYInt64Rows(filepath.Join(staging, config.Audio8VoiceCodesFile), codes); err != nil {
		return fmt.Errorf("audio8: write codes for %q: %w", meta.Name, err)
	}
	blob, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("audio8: encode metadata for %q: %w", meta.Name, err)
	}
	if err := os.WriteFile(filepath.Join(staging, config.Audio8VoiceMetaFile), append(blob, '\n'), 0o600); err != nil {
		return fmt.Errorf("audio8: write metadata for %q: %w", meta.Name, err)
	}

	// Replacement is rename-aside, rename-in, drop-aside. Deleting the old
	// voice first and renaming the new one into the gap would mean that any
	// failure in that window - a crash, a SIGINT, a rename error - left no
	// voice at all, and the staging cleanup above would take the replacement
	// with it. This is the one thing under models_dir that cannot be
	// re-downloaded, so the previous voice stays on disk until the new one is
	// installed, and comes back if the install fails.
	target := config.Audio8VoiceDir(modelsDir, meta.Name)
	aside, err := os.MkdirTemp(root, "."+meta.Name+".old.")
	if err != nil {
		return fmt.Errorf("audio8: stage replacement of voice %q: %w", meta.Name, err)
	}

	// Into a fresh directory rather than onto it: renaming onto an existing
	// path has different rules per platform, and this has none of them.
	saved := filepath.Join(aside, "voice")
	replacing := true
	if err := audio8Rename(target, saved); err != nil {
		if !os.IsNotExist(err) {
			os.RemoveAll(aside)
			return fmt.Errorf("audio8: set aside existing voice %q: %w", meta.Name, err)
		}
		replacing = false // a first registration, nothing to preserve
	}
	if err := audio8Rename(staging, target); err != nil {
		if replacing {
			if restore := audio8Rename(saved, target); restore != nil {
				// Deliberately leaving the set-aside copy on disk: it is now
				// the only one, and removing it here would be exactly the data
				// loss this path exists to prevent. Name it so it can be
				// recovered by hand.
				return fmt.Errorf("audio8: install voice %q: %w (the previous voice is preserved at %s)",
					meta.Name, err, saved)
			}
		}
		os.RemoveAll(aside)
		return fmt.Errorf("audio8: install voice %q: %w", meta.Name, err)
	}
	os.RemoveAll(aside)
	return nil
}

// audio8Rename is os.Rename, indirected so the replacement window above can be
// tested. The guarantee it makes - a failed install leaves the previous voice
// in place - is otherwise only reachable by killing the process mid-rename.
var audio8Rename = os.Rename

// LoadAudio8VoiceMeta reads one voice's metadata.
func LoadAudio8VoiceMeta(modelsDir, name string) (Audio8VoiceMeta, error) {
	canonical, err := config.CanonicalAudio8VoiceName(name)
	if err != nil {
		return Audio8VoiceMeta{}, err
	}
	return readAudio8VoiceMeta(config.Audio8VoiceDir(modelsDir, canonical), canonical)
}

func readAudio8VoiceMeta(dir, name string) (Audio8VoiceMeta, error) {
	raw, err := os.ReadFile(filepath.Join(dir, config.Audio8VoiceMetaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Audio8VoiceMeta{}, fmt.Errorf("%w: %s", ErrAudio8VoiceNotFound, name)
		}
		return Audio8VoiceMeta{}, fmt.Errorf("audio8: read voice %q: %w", name, err)
	}
	var meta Audio8VoiceMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Audio8VoiceMeta{}, fmt.Errorf("audio8: parse %s for voice %q: %w", config.Audio8VoiceMetaFile, name, err)
	}
	// The directory name is the identity; a metadata file that disagrees with
	// it would let a voice answer to a name it is not stored under.
	if meta.Name != name {
		return Audio8VoiceMeta{}, fmt.Errorf("audio8: voice directory %q holds metadata named %q", name, meta.Name)
	}
	return meta, nil
}

// ListAudio8Voices returns every registered voice under modelsDir, in the order
// config.Audio8RegisteredVoiceNames lists them. A malformed voice.json is an
// error rather than a skipped row: a voice the user registered and can no
// longer see is worse than a listing that says why.
func ListAudio8Voices(modelsDir string) ([]Audio8VoiceMeta, error) {
	names := config.Audio8RegisteredVoiceNames(modelsDir)
	out := make([]Audio8VoiceMeta, 0, len(names))
	for _, name := range names {
		meta, err := readAudio8VoiceMeta(config.Audio8VoiceDir(modelsDir, name), name)
		if err != nil {
			return nil, err
		}
		out = append(out, meta)
	}
	return out, nil
}

// RemoveAudio8Voice deletes one registered voice. The packaged reference lives
// inside the export and is not removable through here; ValidateAudio8VoiceName
// rejects its name before any path is built.
func RemoveAudio8Voice(modelsDir, name string) error {
	canonical, err := config.CanonicalAudio8VoiceName(name)
	if err != nil {
		return err
	}
	dir := config.Audio8VoiceDir(modelsDir, canonical)
	if _, err := os.Stat(filepath.Join(dir, config.Audio8VoiceMetaFile)); err != nil {
		return fmt.Errorf("%w: %s", ErrAudio8VoiceNotFound, canonical)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("audio8: remove voice %q: %w", canonical, err)
	}
	return nil
}

// loadAudio8VoiceCodes reads a registered voice's codes, refusing any whose
// fingerprint does not match the installed export. The check happens on load as
// well as on registration because the export can be replaced under a store that
// was written against the previous revision.
func loadAudio8VoiceCodes(modelsDir, name, fingerprint string, numCodebooks int) ([][]int64, Audio8VoiceMeta, error) {
	meta, err := LoadAudio8VoiceMeta(modelsDir, name)
	if err != nil {
		return nil, Audio8VoiceMeta{}, err
	}
	if err := checkAudio8Fingerprint(fingerprint, meta.ModelFingerprint, fmt.Sprintf("registered voice %q", name)); err != nil {
		return nil, Audio8VoiceMeta{}, err
	}
	codes, err := loadNPYInt64Rows(filepath.Join(config.Audio8VoiceDir(modelsDir, name), config.Audio8VoiceCodesFile), numCodebooks)
	if err != nil {
		return nil, Audio8VoiceMeta{}, fmt.Errorf("audio8: voice %q: %w", name, err)
	}
	if len(codes[0]) != meta.Frames {
		return nil, Audio8VoiceMeta{}, fmt.Errorf("audio8: voice %q declares %d frames but its codes hold %d",
			name, meta.Frames, len(codes[0]))
	}
	return codes, meta, nil
}

// audio8VoiceCatalog is the voice list for a store, packaged reference first.
// It reads voice.json files only - no graph is opened - so `voices --json`, the
// Settings picker and persona validation can all list registered voices on a
// machine that could not afford to load the model.
func audio8VoiceCatalog(modelsDir, locale, gender string) ([]Voice, error) {
	voices := staticAudio8Voices(locale, gender)
	if modelsDir == "" {
		return voices, nil
	}
	metas, err := ListAudio8Voices(modelsDir)
	if err != nil {
		return nil, err
	}
	for _, meta := range metas {
		v := Voice{
			Name:         meta.Name,
			FriendlyName: audio8FriendlyName(meta),
			Gender:       audio8VoiceKind(meta),
			Locale:       meta.Locale,
		}
		if locale != "" && !strings.HasPrefix(strings.ToLower(v.Locale), strings.ToLower(locale)) {
			continue
		}
		if gender != "" && !strings.EqualFold(gender, v.Gender) {
			continue
		}
		voices = append(voices, v)
	}
	return voices, nil
}

func audio8VoiceKind(meta Audio8VoiceMeta) string {
	if meta.Synthetic() {
		return Audio8VoiceSynthetic
	}
	return Audio8VoiceRecorded
}

func audio8FriendlyName(meta Audio8VoiceMeta) string {
	if meta.Synthetic() {
		source := strings.TrimSpace(meta.Provenance.BootstrapVoice)
		if provider := strings.TrimSpace(meta.Provenance.BootstrapProvider); provider != "" && source != "" {
			return fmt.Sprintf("Audio8 %s (synthesized from %s %s)", meta.Name, provider, source)
		}
		return fmt.Sprintf("Audio8 %s (synthesized reference)", meta.Name)
	}
	return fmt.Sprintf("Audio8 %s (registered reference)", meta.Name)
}
