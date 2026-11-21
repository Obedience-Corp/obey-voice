package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSTTModelSettingMatchesProviderModeAndManifest(t *testing.T) {
	for _, tc := range []struct{ provider, mode, key, model string }{
		{"", "", "parakeet_model", "tdt-0.6b-v2"},
		{"parakeet", "", "parakeet_model", "tdt-0.6b-v3"},
		{"sherpa-offline", "", "whisper_model", "tiny"},
		{"sherpa", "streaming", "sherpa_streaming_model", "en-2023-06-26"},
		{"whispercpp", "", "whispercpp_model", "small.en"},
		{"transcribecpp", "", "transcribecpp_model", DefaultTranscribeCPPModel},
	} {
		t.Run(tc.provider+tc.mode+tc.model, func(t *testing.T) {
			cfg := &Config{STTProvider: tc.provider, STTMode: tc.mode}
			if err := SetSTTModel(cfg, tc.key, tc.model); err != nil {
				t.Fatal(err)
			}
			spec, model, err := STTModelSetting(cfg)
			if err != nil || spec.Key != tc.key || model != tc.model {
				t.Fatalf("setting = %s/%s, %v", spec.Key, model, err)
			}
			manifest, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
			if err != nil || len(manifest.Assets) == 0 {
				t.Fatalf("no installable selection: %v", err)
			}
		})
	}
}

func TestSTTModelSchemaIncludesMultilingualLabelAndFreeformWhisper(t *testing.T) {
	spec, _ := SpecFor("parakeet_model")
	body, _ := json.Marshal(spec)
	if !strings.Contains(string(body), `"tdt-0.6b-v3":"Multilingual (25 languages)`) {
		t.Fatal(string(body))
	}
	if spec.Default != "tdt-0.6b-v2" {
		t.Fatal("English default changed")
	}
	whisper, _ := SpecFor("whisper_model")
	if whisper.Type != TypeString || len(whisper.Suggestions) == 0 {
		t.Fatal("Whisper must offer models while retaining custom-name support")
	}
}
