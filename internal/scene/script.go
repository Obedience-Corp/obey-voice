package scene

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// scriptRecord identifies the input a run was made from.
type scriptRecord struct {
	Source string // absolute path when it can be resolved
	SHA256 string
}

// writeScripts records what was recorded, next to the audio: the input
// document byte-for-byte, and the resolved script.
//
// The resolved script is always verbatim with concrete turns, so a human can
// edit a line and re-run without regenerating — and so generated mode, when it
// lands, writes the same file this renderer already reads.
func writeScripts(req Request) (scriptRecord, error) {
	data, err := os.ReadFile(req.Options.Script)
	if err != nil {
		return scriptRecord{}, fmt.Errorf("scene: reading script: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(req.Options.OutDir, ScriptFile), data); err != nil {
		return scriptRecord{}, err
	}

	resolved := *req.Doc
	resolved.Mode = ModeVerbatim
	out, err := yaml.Marshal(resolved)
	if err != nil {
		return scriptRecord{}, fmt.Errorf("scene: writing %s: %w", ResolvedScriptFile, err)
	}
	if err := writeFileAtomic(filepath.Join(req.Options.OutDir, ResolvedScriptFile), out); err != nil {
		return scriptRecord{}, err
	}

	sum := sha256.Sum256(data)
	return scriptRecord{Source: absOrAsGiven(req.Options.Script), SHA256: hex.EncodeToString(sum[:])}, nil
}

// absOrAsGiven resolves a path for the manifest, falling back to what the
// operator typed when the working directory cannot be read.
func absOrAsGiven(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
