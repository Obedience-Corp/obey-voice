package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultParakeetModel is the parakeet_model used when the key is unset:
// English, punctuated and cased, the most accurate of the three.
const DefaultParakeetModel = "tdt-0.6b-v2"

// ParakeetAsset describes a bundled NVIDIA Parakeet TDT model released through
// sherpa-onnx as an offline NeMo transducer (encoder/decoder/joiner + tokens).
// All bundled variants are int8 archives.
type ParakeetAsset struct {
	Name          string // parakeet_model value, e.g. tdt-0.6b-v2
	Languages     string // what the model transcribes, for pickers and docs
	URL           string
	ArchiveSHA256 string // checksum of the compressed release archive
	DirName       string // directory the archive extracts to under the models dir
}

const parakeetReleaseBase = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/"

var parakeetModels = map[string]ParakeetAsset{
	"tdt-0.6b-v2": {
		Name:          "tdt-0.6b-v2",
		Languages:     "English",
		URL:           parakeetReleaseBase + "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
		ArchiveSHA256: "157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad",
		DirName:       "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8",
	},
	"tdt-0.6b-v3": {
		Name:          "tdt-0.6b-v3",
		Languages:     "25 European languages incl. English",
		URL:           parakeetReleaseBase + "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2",
		ArchiveSHA256: "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf",
		DirName:       "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8",
	},
	"tdt-110m": {
		Name:          "tdt-110m",
		Languages:     "English (small, fast)",
		URL:           parakeetReleaseBase + "sherpa-onnx-nemo-parakeet_tdt_transducer_110m-en-36000-int8.tar.bz2",
		ArchiveSHA256: "f628312e9fdf8686374cb01a69425c41732529d540860311f16f37cbc32cfe9b",
		DirName:       "sherpa-onnx-nemo-parakeet_tdt_transducer_110m-en-36000-int8",
	},
}

// ParakeetModelNames lists the accepted parakeet_model values, sorted.
func ParakeetModelNames() []string {
	names := make([]string, 0, len(parakeetModels))
	for name := range parakeetModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ParakeetModel resolves a parakeet_model value; empty selects the default.
func ParakeetModel(name string) (ParakeetAsset, error) {
	key := strings.TrimSpace(strings.ToLower(name))
	if key == "" {
		key = DefaultParakeetModel
	}
	asset, ok := parakeetModels[key]
	if !ok {
		return ParakeetAsset{}, fmt.Errorf("unsupported parakeet_model %q; choose one of %s", name, strings.Join(ParakeetModelNames(), ", "))
	}
	return asset, nil
}

// ModelDir joins the asset directory to the base models dir.
func (a ParakeetAsset) ModelDir(base string) string { return filepath.Join(base, a.DirName) }

// Encoder, Decoder, Joiner and Tokens name the files sherpa-onnx loads.
func (a ParakeetAsset) Encoder() string { return "encoder.int8.onnx" }
func (a ParakeetAsset) Decoder() string { return "decoder.int8.onnx" }
func (a ParakeetAsset) Joiner() string  { return "joiner.int8.onnx" }
func (a ParakeetAsset) Tokens() string  { return "tokens.txt" }

// EnglishOnly reports whether the model transcribes English only.
func (a ParakeetAsset) EnglishOnly() bool { return a.Name != "tdt-0.6b-v3" }

// RequiredFiles lists the files that must exist after extraction.
func (a ParakeetAsset) RequiredFiles() []string {
	return []string{a.Encoder(), a.Decoder(), a.Joiner(), a.Tokens()}
}
