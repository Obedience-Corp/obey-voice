package tts

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// ProviderSpec describes a TTS provider compiled into this build.
type ProviderSpec struct {
	Name        string
	Description string
}

var providerSpecs = []ProviderSpec{
	{Name: "kokoro", Description: "Local Kokoro TTS"},
	{Name: "qwen3-tts", Description: "Qwen3-TTS preset voices (native worker preferred; managed Python legacy)"},
	// audio8 was held out of this catalog while it had no tokenizer and could
	// never speak. It now speaks wherever its export is installed, so it is
	// listed on qwen3-tts's terms: the catalog says what this build implements,
	// and the not-installed case is reported where installs are known —
	// Settings marks the row via Audio8Installed, and NewProvider names the
	// missing file — rather than by hiding the row.
	{Name: "audio8", Description: "Audio8 DualAR ONNX (experimental · quality preview · packaged reference voice)"},
}

// Providers returns the list of implemented TTS providers.
func Providers() []ProviderSpec {
	out := make([]ProviderSpec, len(providerSpecs))
	copy(out, providerSpecs)
	return out
}

// NewProvider constructs the configured TTS provider and its cleanup hook.
func NewProvider(cfg *config.Config) (Provider, func(), error) {
	switch strings.TrimSpace(strings.ToLower(cfg.TTSProvider)) {
	case "", "kokoro":
		provider, err := NewKokoroTTS(cfg)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case "qwen3-tts":
		provider, err := NewQwen3TTS(cfg)
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	case audio8ProviderName:
		modelDir := audio8ModelDir(cfg)
		tok, err := NewAudio8Tokenizer(modelDir)
		if err != nil {
			// Before the manifest and the graphs: a missing or malformed
			// tokenizer.json means this provider can never speak, and saying so
			// costs nothing instead of seconds and gigabytes.
			return nil, nil, err
		}
		provider, err := NewAudio8TTS(Audio8Options{
			ModelDir:  modelDir,
			ModelsDir: config.ModelsDirFrom(cfg),
			Voice:     cfg.Audio8Voice,
			Tokenizer: tok,
		})
		if err != nil {
			return nil, nil, err
		}
		return provider, provider.Delete, nil
	default:
		return nil, nil, unsupportedProviderError(cfg.TTSProvider)
	}
}

// StaticVoices returns the compile-time voice catalog for providerName,
// filtered by locale/gender. Every provider's live ListVoices is built from
// this same catalog (kokoro directly; qwen3-tts's CustomVoice mode from the
// same qwen.CustomVoices() registry), so the two can never drift, and a
// caller that must never construct a provider or touch a model file — e.g.
// `voices --json` on a fresh machine — can still list real voice names.
func StaticVoices(providerName, locale, gender string) ([]Voice, error) {
	return StaticVoicesFor(nil, providerName, locale, gender)
}

// StaticVoicesFor is StaticVoices with a config, which audio8 needs and no
// other provider does: its catalog is the packaged reference plus whatever the
// user registered under models_dir, so the answer depends on which models_dir
// this config points at. Every caller that has a config should use this one -
// StaticVoices without one can only ever report audio8's packaged voice.
func StaticVoicesFor(cfg *config.Config, providerName, locale, gender string) ([]Voice, error) {
	switch strings.TrimSpace(strings.ToLower(providerName)) {
	case "", "kokoro":
		return staticKokoroVoices(locale, gender), nil
	case "qwen3-tts":
		return staticQwenVoices(locale, gender), nil
	case audio8ProviderName:
		if cfg == nil {
			return staticAudio8Voices(locale, gender), nil
		}
		return audio8VoiceCatalog(config.ModelsDirFrom(cfg), locale, gender)
	default:
		return nil, unsupportedProviderError(providerName)
	}
}

func staticKokoroVoices(locale, gender string) []Voice {
	var voices []Voice
	for _, name := range voiceNames {
		if len(name) < 3 || !strings.Contains(name, "_") {
			continue
		}
		vLocale := langMap[name[0]]
		vGender := genderMap[name[1]]
		vName := strings.SplitN(name, "_", 2)[1]

		if locale != "" && !strings.HasPrefix(vLocale, locale) {
			continue
		}
		if gender != "" && !strings.EqualFold(vGender, gender) {
			continue
		}

		voices = append(voices, Voice{
			Name:         name,
			FriendlyName: fmt.Sprintf("Kokoro %s (%s)", titleCase(vName), vLocale),
			Gender:       vGender,
			Locale:       vLocale,
		})
	}
	return voices
}

// staticQwenVoices maps selectable Qwen voices (model presets + shipped clones).
// They exist whether or not the native package is installed, so a picker can
// list them before anything has been downloaded. Gender is not something the
// preset registry tracks per voice, so every row reports "preset" — the same
// convention internal/tui/settings_qwen.go already uses for this exact
// mapping — rather than guessing from the description text.
func staticQwenVoices(locale, gender string) []Voice {
	const presetGender = "preset"
	if gender != "" && !strings.EqualFold(gender, presetGender) {
		return nil
	}
	var voices []Voice
	for _, v := range managedqwen.SelectableVoices() {
		if locale != "" && !strings.HasPrefix(v.NativeLanguage, locale) {
			continue
		}
		voices = append(voices, Voice{
			Name:         v.Name,
			FriendlyName: v.Description,
			Gender:       presetGender,
			Locale:       v.NativeLanguage,
		})
	}
	return voices
}

func unsupportedProviderError(configured string) error {
	names := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		names = append(names, spec.Name)
	}
	return fmt.Errorf("unsupported tts_provider %q (implemented providers: %s)", configured, strings.Join(names, ", "))
}

// titleCase upper-cases the first letter of each word — a small ASCII stand-in
// for the deprecated strings.Title, used only for Kokoro voice display names.
// Matches strings.Title's word boundaries: letters, digits, and '_' are word
// characters, so only a letter following a separator (or the start) is raised.
func titleCase(s string) string {
	prevIsWord := false
	return strings.Map(func(r rune) rune {
		isWord := r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z')
		if !prevIsWord && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		prevIsWord = isWord
		return r
	}, s)
}
