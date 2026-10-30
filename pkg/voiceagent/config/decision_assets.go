package config

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
)

func ManagedDecisions(cfg *Config) bool {
	return cfg != nil && cfg.DecisionEnabled && (cfg.DecisionVoiceWork || cfg.DecisionTextWork) &&
		(cfg.DecisionProvider == "" || cfg.DecisionProvider == "local") && (cfg.DecisionRuntime == "" || cfg.DecisionRuntime == "managed")
}

func decisionAssets(cfg *Config) ([]Asset, error) {
	model, err := decider.ModelFor(cfg.DecisionLocalModel)
	if err != nil {
		return nil, err
	}
	if _, err := decider.DeviceFor(runtime.GOOS, runtime.GOARCH, cfg.DecisionDevice, false); err != nil {
		return nil, err
	}
	uvName, uvSHA, err := decider.UVAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	weights := Asset{ID: "decision." + model.Revision, Provider: "decider", Kind: AssetKindDecision, Name: model.Name, License: model.License, Source: "https://huggingface.co/" + model.Repo}
	for _, file := range model.Files {
		weights.Files = append(weights.Files, AssetFile{Path: filepath.Join("decider", "models", model.Revision, file.Name), URL: fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", model.Repo, model.Revision, file.Name), SHA256: file.SHA256, Size: file.Size})
	}
	assets := []Asset{weights,
		{ID: "decision.runtime.source", Provider: "decider", Kind: AssetKindDecision, Name: "Decision runtime source", License: "Apache-2.0", Source: "https://github.com/Mapika/decider", Files: []AssetFile{{Path: filepath.Join("decider", "decider-"+decider.RuntimeRevision+".tar.gz"), URL: "https://github.com/Mapika/decider/archive/" + decider.RuntimeRevision + ".tar.gz", SHA256: decider.RuntimeSHA256}}},
		{ID: "decision.uv." + decider.UVVersion + "." + runtime.GOOS + "." + runtime.GOARCH, Provider: "decider", Kind: AssetKindDecision, Name: "Decision runtime installer", License: "MIT OR Apache-2.0", Source: "https://github.com/astral-sh/uv", TargetDir: filepath.Join("decider", "uv-"+decider.UVVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH), Archive: &AssetArchive{URL: "https://github.com/astral-sh/uv/releases/download/" + decider.UVVersion + "/" + uvName + ".tar.gz", SHA256: uvSHA}, CheckFiles: []string{"uv"}},
	}
	assets[2].decisionModel = cfg.DecisionLocalModel
	assets[2].decisionDevice = cfg.DecisionDevice
	assets[2].decisionRuntime = true
	if model.Engine == "liquid" {
		assets = append(assets[:1], assets[2:]...)
	}
	return assets, nil
}
