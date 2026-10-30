package qwen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed voices/veronica/ref.wav
var veronicaRefWAV []byte

//go:embed voices/veronicadesk/ref.wav
var veronicaDeskRefWAV []byte

// ShippedVoice is a packaged clone throat, not a model-native CustomVoice preset.
// Native qwen3-tts-worker speaks it via ref_wav.
type ShippedVoice struct {
	Name        string
	ID          string
	Language    string
	Description string
	RefText     string
	wav         []byte
}

var shippedVoices = []ShippedVoice{
	{
		Name:        "Veronica",
		ID:          "veronica",
		Language:    "English",
		Description: "Booth reference voice. Source-clip clone.",
		RefText:     "Just like that, feel the rhythm of my voice.",
		wav:         veronicaRefWAV,
	},
	{
		Name:        "VeronicaDesk",
		ID:          "veronicadesk",
		Language:    "English",
		Description: "Desk throat. Join-clip clone, talking to one person.",
		RefText:     "I'm Veronica. I just joined Obedience Corp. I'll be building the voice you actually talk to.",
		wav:         veronicaDeskRefWAV,
	},
}

// ShippedVoices returns packaged clone throats.
func ShippedVoices() []ShippedVoice {
	out := make([]ShippedVoice, len(shippedVoices))
	copy(out, shippedVoices)
	return out
}

func (s ShippedVoice) cacheID() string {
	if id := strings.TrimSpace(s.ID); id != "" {
		return strings.ToLower(id)
	}
	return shippedKey(s.Name)
}

func shippedKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	return value
}

// LookupShipped resolves a case-insensitive shipped clone name or id.
func LookupShipped(value string) (ShippedVoice, bool) {
	key := shippedKey(value)
	if key == "" {
		return ShippedVoice{}, false
	}
	for _, voice := range shippedVoices {
		if shippedKey(voice.Name) == key || shippedKey(voice.ID) == key {
			return voice, true
		}
	}
	return ShippedVoice{}, false
}

// SelectableVoices is the picker catalog: model presets plus shipped clones.
func SelectableVoices() []Voice {
	out := CustomVoices()
	for _, s := range shippedVoices {
		out = append(out, Voice{
			Name:           s.Name,
			NativeLanguage: s.Language,
			Description:    s.Description,
		})
	}
	return out
}

// ResolveVoice maps a user/config value to a preset or shipped clone name.
func ResolveVoice(value string) (name string, shipped bool, ok bool) {
	if v, found := CanonicalVoice(value); found {
		return v, false, true
	}
	if s, found := LookupShipped(value); found {
		return s.Name, true, true
	}
	return "", false, false
}

// MaterializeShipped writes the embedded WAV under modelsDir if missing or stale.
func MaterializeShipped(ctx context.Context, voice ShippedVoice, modelsDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(voice.wav) == 0 {
		return "", fmt.Errorf("qwen shipped voice %q has no embedded reference", voice.Name)
	}
	root := strings.TrimSpace(modelsDir)
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("qwen shipped voice cache: %w", err)
		}
		root = filepath.Join(cache, "obey-voice")
	}
	dest := filepath.Join(root, "voices", voice.cacheID(), "ref.wav")
	if sameShippedFile(dest, voice.wav) {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("qwen shipped voice dir: %w", err)
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, voice.wav, 0o644); err != nil {
		return "", fmt.Errorf("qwen shipped voice write: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("qwen shipped voice publish: %w", err)
	}
	return dest, nil
}

func sameShippedFile(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(got) != len(want) {
		return false
	}
	sumWant := sha256.Sum256(want)
	sumGot := sha256.Sum256(got)
	return bytes.Equal(sumWant[:], sumGot[:])
}
