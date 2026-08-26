package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultTranscribeCPPModel is the transcribecpp_model used when the key is
// unset. Unlike parakeet_model's tdt-0.6b-v2, this is upstream's full Hugging
// Face repo slug — the GGUF repository is named for it — so the two keys read
// differently on purpose.
const DefaultTranscribeCPPModel = "parakeet-tdt-0.6b-v2"

// transcribeCPPSubdir is where managed GGUF weights are cached under the models
// dir, mirroring the whispercpp/ nesting.
const transcribeCPPSubdir = "transcribecpp"

const transcribeCPPGGUFBase = "https://huggingface.co/handy-computer/"

// TranscribeCPPAsset describes one GGUF weight the transcribe.cpp CLI loads.
// The URL resolves the repo's main branch, so the revision is not pinned
// upstream; SHA256/Size are what pin the content, and a re-tagged file fails
// verification rather than installing silently.
//
// The transcribe.cpp runtime is MIT but the weights are separately licensed,
// and shipping them through 'models ensure' is what triggers the obligation, so
// License travels with the asset rather than living in a comment.
type TranscribeCPPAsset struct {
	Name      string // transcribecpp_model value
	Languages string // what the model transcribes, for pickers and docs
	Filename  string
	URL       string
	SHA256    string
	Size      int64
	License   string // weight license, e.g. cc-by-4.0 (attribution)
	Source    string // where the weights come from, named alongside License
}

var transcribeCPPModels = map[string]TranscribeCPPAsset{
	DefaultTranscribeCPPModel: {
		Name:      DefaultTranscribeCPPModel,
		Languages: "English",
		Filename:  "parakeet-tdt-0.6b-v2-Q8_0.gguf",
		URL:       transcribeCPPGGUFBase + "parakeet-tdt-0.6b-v2-gguf/resolve/main/parakeet-tdt-0.6b-v2-Q8_0.gguf",
		SHA256:    "f0d0e99cebb6d3b83f1f7069b82b5d3c2e39a54545b0da039cb4bafd9c4e5caa",
		Size:      729574912,
		License:   "cc-by-4.0",
		Source:    transcribeCPPGGUFBase + "parakeet-tdt-0.6b-v2-gguf",
	},
}

// TranscribeCPPModelNames lists the accepted transcribecpp_model values, sorted.
func TranscribeCPPModelNames() []string {
	names := make([]string, 0, len(transcribeCPPModels))
	for name := range transcribeCPPModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TranscribeCPPModel resolves a transcribecpp_model value; empty selects the
// default.
func TranscribeCPPModel(name string) (TranscribeCPPAsset, error) {
	key := strings.TrimSpace(strings.ToLower(name))
	if key == "" {
		key = DefaultTranscribeCPPModel
	}
	asset, ok := transcribeCPPModels[key]
	if !ok {
		return TranscribeCPPAsset{}, fmt.Errorf("unsupported transcribe.cpp model %q; choose one of %s", name, strings.Join(TranscribeCPPModelNames(), ", "))
	}
	return asset, nil
}

// RelPath is the GGUF's path relative to the models dir.
func (a TranscribeCPPAsset) RelPath() string {
	return filepath.Join(transcribeCPPSubdir, a.Filename)
}

// ModelPath joins the GGUF under the models dir at base.
func (a TranscribeCPPAsset) ModelPath(base string) string {
	return filepath.Join(base, a.RelPath())
}
