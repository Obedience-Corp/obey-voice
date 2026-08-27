package tts

import (
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

func TestNewProviderRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{TTSProvider: "edge"}

	_, _, err := NewProvider(cfg)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported tts_provider") {
		t.Fatalf("NewProvider() error = %q, want unsupported tts_provider message", err)
	}
}

// Selecting audio8 must fail on the missing tokenizer before any model
// loading. ModelsDir points at an empty directory: reaching the manifest or
// the graphs would surface a missing-file error instead of
// errAudio8NoTokenizer, so the sentinel proves the rejection happened first.
func TestNewProviderAudio8FailsFastWithoutTokenizer(t *testing.T) {
	cfg := &config.Config{TTSProvider: "audio8", ModelsDir: t.TempDir()}

	_, _, err := NewProvider(cfg)
	if !errors.Is(err, errAudio8NoTokenizer) {
		t.Fatalf("NewProvider() error = %v, want errAudio8NoTokenizer", err)
	}
}

func TestStaticVoicesForKokoro(t *testing.T) {
	voices, err := StaticVoices("kokoro", "en-US", "female")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	if len(voices) == 0 {
		t.Fatal("StaticVoices() returned no voices, want at least one")
	}
}

func TestProvidersIncludesOptionalQwen(t *testing.T) {
	for _, spec := range Providers() {
		if spec.Name == "qwen3-tts" {
			return
		}
	}
	t.Fatalf("Providers() = %+v, missing qwen3-tts", Providers())
}

// The 9 CustomVoice presets are model-native, not install-dependent: they
// exist whether or not the native package (or any tier of it) is on disk, so
// StaticVoices reports them unconditionally — a picker can list qwen voices
// before a single byte has been downloaded.
func TestStaticVoicesForQwenListsCustomVoicePresets(t *testing.T) {
	voices, err := StaticVoices("qwen3-tts", "", "")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	if len(voices) != len(managedqwen.SelectableVoices()) {
		t.Fatalf("StaticVoices() = %d voices, want %d (selectable catalog)", len(voices), len(managedqwen.SelectableVoices()))
	}
	var sawVivian bool
	for _, v := range voices {
		if v.Name != "Vivian" {
			continue
		}
		sawVivian = true
		if v.Gender != "preset" {
			t.Errorf("Vivian gender = %q, want the preset-registry convention %q", v.Gender, "preset")
		}
		if v.Locale != "Chinese" {
			t.Errorf("Vivian locale = %q, want its native language %q", v.Locale, "Chinese")
		}
		if v.FriendlyName == "" {
			t.Error("Vivian friendly_name is empty, want the preset description")
		}
	}
	if !sawVivian {
		t.Fatalf("StaticVoices() = %+v, missing the Vivian preset", voices)
	}
	var sawVeronica bool
	for _, v := range voices {
		if v.Name == "Veronica" {
			sawVeronica = true
			if v.Locale != "English" {
				t.Errorf("Veronica locale = %q, want English", v.Locale)
			}
		}
	}
	if !sawVeronica {
		t.Fatalf("StaticVoices() = %+v, missing shipped Veronica", voices)
	}
}

func TestStaticVoicesForQwenHonoursLocaleFilter(t *testing.T) {
	voices, err := StaticVoices("qwen3-tts", "English", "")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	if len(voices) == 0 {
		t.Fatal("StaticVoices() returned no English voices, want at least Ryan/Aiden")
	}
	for _, v := range voices {
		if v.Locale != "English" {
			t.Errorf("voice %q locale = %q, want English (filter should exclude the rest)", v.Name, v.Locale)
		}
	}
}

func TestStaticVoicesForQwenGenderFilterExcludesUnknownValues(t *testing.T) {
	voices, err := StaticVoices("qwen3-tts", "", "female")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	// The preset registry has no per-voice gender data (every entry reports
	// "preset"), so a caller filtering by a real gender value gets no rows —
	// never a silently-wrong match.
	if len(voices) != 0 {
		t.Fatalf("StaticVoices(gender=female) = %+v, want none (the registry has no per-voice gender)", voices)
	}
}

// KokoroTTS.ListVoices must never drift from StaticVoices("kokoro", ...):
// they are required to be the same code path.
func TestKokoroListVoicesMatchesStaticVoices(t *testing.T) {
	k := &KokoroTTS{}
	want, err := StaticVoices("kokoro", "en", "female")
	if err != nil {
		t.Fatalf("StaticVoices() error = %v", err)
	}
	got := k.ListVoices("en", "female")
	if len(got) == 0 {
		t.Fatal("KokoroTTS.ListVoices() returned nothing, want at least one en/female voice")
	}
	if len(got) != len(want) {
		t.Fatalf("KokoroTTS.ListVoices() = %d voices, StaticVoices() = %d, want equal", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("KokoroTTS.ListVoices()[%d] = %+v, StaticVoices()[%d] = %+v, want identical", i, got[i], i, want[i])
		}
	}
}
