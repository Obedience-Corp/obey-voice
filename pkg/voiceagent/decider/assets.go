// Package decider owns the optional managed local decision runtime.
package decider

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
)

const RuntimeRevision = "620d2225cc436ab0b001e3200e590e3b286ef9ab"
const RuntimeVersion = "b11669"

//go:embed models.json
var modelJSON []byte

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Model struct {
	NativeRepo        string             `json:"native_repo"`
	NativeRevision    string             `json:"native_revision"`
	Temperature       float64            `json:"temperature"`
	TemperatureByType map[string]float64 `json:"temperature_by_type"`
	Name              string             `json:"name"`
	Engine            string             `json:"engine"`
	License           string             `json:"license"`
	MemoryGiB         int                `json:"memory_gib"`
	Repo              string             `json:"repo"`
	Revision          string             `json:"revision"`
	Files             []File             `json:"files"`
}

func ModelFor(tier string) (Model, error) {
	if tier == "" {
		tier = "2b"
	}
	var models map[string]Model
	if err := json.Unmarshal(modelJSON, &models); err != nil {
		return Model{}, err
	}
	model, ok := models[tier]
	if !ok {
		return Model{}, fmt.Errorf("unknown local decision model %q", tier)
	}
	return model, nil
}

func ModelDir(modelsDir, tier string) (string, error) {
	model, err := ModelFor(tier)
	if err != nil {
		return "", err
	}
	return filepath.Join(modelsDir, "decider", "native-models", model.NativeRevision), nil
}

//go:embed runtimes.json
var runtimeJSON []byte

type RuntimeAsset struct {
	Name   string        `json:"name"`
	URL    string        `json:"url"`
	SHA256 string        `json:"sha256"`
	Size   int64         `json:"size"`
	CUDA   *RuntimeAsset `json:"cuda,omitempty"`
}

func NativeAsset(goos, arch, device string) (RuntimeAsset, error) {
	var assets map[string]RuntimeAsset
	if err := json.Unmarshal(runtimeJSON, &assets); err != nil {
		return RuntimeAsset{}, err
	}
	key := goos + "/" + arch
	if device == "cuda" {
		key += "/cuda"
	}
	a, ok := assets[key]
	if !ok {
		return a, fmt.Errorf("managed native decisions unavailable on %s (%s)", key, device)
	}
	return a, nil
}
func RuntimeDir(modelsDir, device string) string {
	return filepath.Join(modelsDir, "decider", "native-"+RuntimeVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH+"-"+device)
}
func RuntimeArchivePath(modelsDir string, a RuntimeAsset) string {
	return filepath.Join(modelsDir, "decider", "archives", a.Name)
}
