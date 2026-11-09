package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
)

func ManagedDecisions(cfg *Config) bool {
	return cfg != nil && cfg.DecisionEnabled && (cfg.DecisionVoiceWork || cfg.DecisionTextWork) && (cfg.DecisionProvider == "" || cfg.DecisionProvider == "local") && (cfg.DecisionRuntime == "" || cfg.DecisionRuntime == "managed")
}
func decisionAssets(cfg *Config) ([]Asset, error) {
	model, err := decider.ModelFor(cfg.DecisionLocalModel)
	if err != nil {
		return nil, err
	}
	device, err := decider.DeviceFor(runtime.GOOS, runtime.GOARCH, cfg.DecisionDevice, false)
	if err != nil {
		return nil, err
	}
	// Status must describe the runtime installed by auto preparation without
	// running a GPU probe in the settings render path. Preparation passes its
	// explicitly resolved device and never uses this installed-state hint.
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && (cfg.DecisionDevice == "" || cfg.DecisionDevice == "auto") {
		if _, err := os.Stat(filepath.Join(decider.RuntimeDir(cfg.ModelsDir, "cuda"), "installed.json")); err == nil {
			device = "cuda"
		}
	}
	a, err := decider.NativeAsset(runtime.GOOS, runtime.GOARCH, device)
	if err != nil {
		return nil, err
	}
	weights := Asset{ID: "decision.native." + model.NativeRevision, Provider: "decider", Kind: AssetKindDecision, Name: model.Name, License: model.License, Source: "https://huggingface.co/" + model.NativeRepo}
	for _, f := range model.Files {
		weights.Files = append(weights.Files, AssetFile{Path: filepath.Join("decider", "native-models", model.NativeRevision, f.Name), URL: fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", model.NativeRepo, model.NativeRevision, f.Name), SHA256: f.SHA256, Size: f.Size})
	}
	assets := []Asset{weights}
	for _, part := range []*decider.RuntimeAsset{&a, a.CUDA} {
		if part == nil {
			continue
		}
		license := "MIT"
		if part == a.CUDA {
			license = "NVIDIA CUDA Toolkit EULA"
		}
		assets = append(assets, Asset{ID: "decision.native.runtime." + part.Name, Name: "Native decision runtime", Provider: "decider", Kind: AssetKindDecision, License: license, Source: "https://github.com/ggml-org/llama.cpp", Files: []AssetFile{{Path: filepath.Join("decider", "archives", part.Name), URL: part.URL, SHA256: part.SHA256, Size: part.Size}}})
	}
	assets[1].decisionRuntime = true
	assets[1].decisionModel = cfg.DecisionLocalModel
	assets[1].decisionDevice = device
	return assets, nil
}
