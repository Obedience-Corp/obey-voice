// Package decider owns the optional managed local decision runtime.
package decider

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
)

const RuntimeRevision = "91b6fa9b94ab6c4dc2c63d3b14d413243b4fffcc"
const RuntimeSHA256 = "2e35b1566396c89e60b53bee4de734390a85ced4e85495f924062fcfa0d7a0d3"
const UVVersion = "0.7.13"

//go:embed models.json
var modelJSON []byte

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Model struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	License   string `json:"license"`
	MemoryGiB int    `json:"memory_gib"`
	Repo      string `json:"repo"`
	Revision  string `json:"revision"`
	Files     []File `json:"files"`
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
	return filepath.Join(modelsDir, "decider", "models", model.Revision), nil
}

func UVAsset(goos, arch string) (name, sha string, err error) {
	assets := map[string][2]string{
		"darwin/arm64": {"uv-aarch64-apple-darwin", "721f532b73171586574298d4311a91d5ea2c802ef4db3ebafc434239330090c6"},
		"linux/amd64":  {"uv-x86_64-unknown-linux-gnu", "909278eb197c5ed0e9b5f16317d1255270d1f9ea4196e7179ce934d48c4c2545"},
		"linux/arm64":  {"uv-aarch64-unknown-linux-gnu", "0b2ad9fe4295881615295add8cc5daa02549d29cc9a61f0578e397efcf12f08f"},
	}
	asset, ok := assets[goos+"/"+arch]
	if !ok {
		return "", "", fmt.Errorf("managed decisions are unavailable on %s/%s; use an external local server", goos, arch)
	}
	return asset[0], asset[1], nil
}

func UVDir(modelsDir string) string {
	return filepath.Join(modelsDir, "decider", "uv-"+UVVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH)
}
func SourcePath(modelsDir string) string {
	return filepath.Join(modelsDir, "decider", "decider-"+RuntimeRevision+".tar.gz")
}
