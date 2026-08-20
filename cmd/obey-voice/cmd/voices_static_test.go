//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// --- voices --json: source + languages (MDL-A5) ---

func TestVoicesJSONSourceIsProviderWhenAssetsPresent(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "provider" {
		t.Errorf("source = %q, want provider (assets present, a live provider was constructed)", got.Source)
	}
	if got.Languages != nil {
		t.Errorf("languages = %v, want omitted for kokoro", got.Languages)
	}
}

func TestVoicesJSONLanguagesFieldOmittedForKokoro(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, kokoroFake(), nil)

	out, err := runVoicesCmd(t, "--json")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if _, present := raw["languages"]; present {
		t.Errorf("raw output has a languages key for kokoro, want it fully omitted: %s", out)
	}
}

func TestVoicesJSONQwenStaticFallbackWhenAssetsMissing(t *testing.T) {
	personaEnv(t)
	var newProviderCalled bool
	orig := voiceStack
	voiceStack = voiceStackDeps{
		newProvider: func(*config.Config) (tts.Provider, func(), error) {
			newProviderCalled = true
			return kokoroFake(), nil, nil
		},
		missingAssets: func(cfg *config.Config) []string {
			if isQwenTTS(cfg.TTSProvider) {
				return []string{"Qwen3-TTS native package"}
			}
			return nil
		},
		ensureAssets: func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error {
			t.Fatal("ensureAssets must never be called by voices --json")
			return nil
		},
	}
	t.Cleanup(func() { voiceStack = orig })

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts")
	if err != nil {
		t.Fatalf("voices --json error = %v, want the static fallback to succeed (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "static" {
		t.Errorf("source = %q, want static (assets missing, no provider constructed)", got.Source)
	}
	if newProviderCalled {
		t.Error("newProvider was called; the static fallback must never construct a provider")
	}
	wantN := len(managedqwen.SelectableVoices())
	if len(got.Voices) != wantN {
		t.Fatalf("voices = %+v, want the %d selectable voices", got.Voices, wantN)
	}
	var sawVeronica bool
	for _, v := range got.Voices {
		if v.Name == "Veronica" {
			sawVeronica = true
		}
	}
	if !sawVeronica {
		t.Fatalf("voices = %+v, missing shipped Veronica", got.Voices)
	}
	if len(got.Languages) == 0 {
		t.Error("languages is empty, want qwen.SupportedLanguages()")
	}
}

func TestVoicesJSONQwenLanguagesPresentOnLiveProviderPath(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, &fakeTTS{voices: []tts.Voice{{Name: "Vivian", FriendlyName: "Vivian", Gender: "preset", Locale: "Chinese"}}}, nil)

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts", "--live")
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "provider" {
		t.Errorf("source = %q, want provider (assets present)", got.Source)
	}
	if len(got.Languages) == 0 {
		t.Error("languages is empty, want qwen.SupportedLanguages() regardless of source")
	}
}

// A provider whose static catalog is genuinely empty (or errors) must keep
// the original assets_missing refusal rather than returning an empty static
// success — the caller still needs a clear signal to run models ensure.
func TestVoicesJSONMissingAssetsStillRefusesForNonQwenProviders(t *testing.T) {
	personaEnv(t)
	ensured := stubVoiceStack(t, kokoroFake(), []string{"Kokoro model"})

	out, err := runVoicesCmd(t, "--json")
	if err == nil {
		t.Fatalf("voices --json error = nil, want assets_missing preserved for kokoro (out %s)", out)
	}
	if code, _, _ := decodeErrorJSON(t, out); code != codeAssetsMissing {
		t.Fatalf("code = %q, want %q", code, codeAssetsMissing)
	}
	if *ensured != 0 {
		t.Fatalf("ensureAssets called %d times, want 0", *ensured)
	}
}

// --- voices --json --provider qwen3-tts: presets without a model load ---

// The macOS voice picker gives the CLI 10s; constructing qwen3-tts to read a
// compile-time preset list measured 10.7s and timed the picker out. The
// default answer is now static, and it must stay that way even on a machine
// where the model is installed and a provider would have succeeded.
func TestVoicesJSONQwenListsPresetsWithoutConstructingProvider(t *testing.T) {
	dir := personaEnv(t)
	writeQwenVoiceConfig(t, dir, "Ryan")

	var constructed int
	orig := voiceStack
	voiceStack = voiceStackDeps{
		newProvider: func(*config.Config) (tts.Provider, func(), error) {
			constructed++
			return kokoroFake(), nil, nil
		},
		missingAssets: func(*config.Config) []string { return nil },
		ensureAssets: func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error {
			t.Fatal("ensureAssets must never be called by voices --json")
			return nil
		},
	}
	t.Cleanup(func() { voiceStack = orig })

	start := time.Now()
	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("voices --json error = %v (out %s)", err, out)
	}
	if constructed != 0 {
		t.Fatalf("newProvider called %d times, want 0: listing presets must not page a model in", constructed)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed = %v, want under 1s (the picker's budget is 10s)", elapsed)
	}

	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "static" {
		t.Errorf("source = %q, want static", got.Source)
	}
	if got.ActiveVoice != "Ryan" {
		t.Errorf("active_voice = %q, want the configured qwen_tts_voice", got.ActiveVoice)
	}
	if len(got.Voices) != len(managedqwen.SelectableVoices()) {
		t.Fatalf("voices = %d, want the %d selectable presets", len(got.Voices), len(managedqwen.SelectableVoices()))
	}
	var active string
	for _, v := range got.Voices {
		if v.Active {
			active = v.Name
		}
	}
	if active != "Ryan" {
		t.Errorf("marked-active voice = %q, want Ryan", active)
	}
}

// --live is the escape hatch: presets a native package ships beyond the
// compile-time registry are still reachable, at the cost of the model load.
func TestVoicesJSONQwenLiveFlagQueriesTheProvider(t *testing.T) {
	personaEnv(t)
	stubVoiceStack(t, &fakeTTS{voices: []tts.Voice{
		{Name: "InstalledOnly", FriendlyName: "Shipped with the package", Gender: "preset", Locale: "English"},
	}}, nil)

	out, err := runVoicesCmd(t, "--json", "--provider", "qwen3-tts", "--live")
	if err != nil {
		t.Fatalf("voices --json --live error = %v (out %s)", err, out)
	}
	var got voicesJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Source != "provider" {
		t.Errorf("source = %q, want provider under --live", got.Source)
	}
	if len(got.Voices) != 1 || got.Voices[0].Name != "InstalledOnly" {
		t.Errorf("voices = %+v, want the provider's own catalog", got.Voices)
	}
}

func writeQwenVoiceConfig(t *testing.T, dir, voice string) {
	t.Helper()
	body := "agent_name: Samantha\nactive_persona: samantha\ntts_provider: kokoro\ntts_voice: af_heart\nqwen_tts_voice: " + voice + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
