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
func writeScripts(req Request) (scriptRecord, error) {
	record, err := copyInputScript(req.Options)
	if err != nil {
		return scriptRecord{}, err
	}
	if err := writeResolvedScript(req); err != nil {
		return scriptRecord{}, err
	}
	return record, nil
}

// copyInputScript keeps the authored document byte-for-byte under the out-dir
// and returns where it came from and what it hashed to.
func copyInputScript(opts Options) (scriptRecord, error) {
	data, err := os.ReadFile(opts.Script)
	if err != nil {
		return scriptRecord{}, fmt.Errorf("scene: reading script: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(opts.OutDir, ScriptFile), data); err != nil {
		return scriptRecord{}, err
	}
	sum := sha256.Sum256(data)
	return scriptRecord{Source: absOrAsGiven(opts.Script), SHA256: hex.EncodeToString(sum[:])}, nil
}

// writeResolvedScript writes script.resolved.yaml: always verbatim, with
// concrete turns and each speaker's effective stack, so a human can edit a line
// and re-run without regenerating, and a generated run leaves the same file
// the renderer already reads.
func writeResolvedScript(req Request) error {
	out, err := yaml.Marshal(resolvedDocument(req))
	if err != nil {
		return fmt.Errorf("scene: writing %s: %w", ResolvedScriptFile, err)
	}
	return writeFileAtomic(filepath.Join(req.Options.OutDir, ResolvedScriptFile), out)
}

// resolvedDocument is the request's document pinned to verbatim, its cast
// annotated with what each persona resolved to. The cast slice is copied so
// the caller's document keeps its authored shape.
func resolvedDocument(req Request) Document {
	resolved := *req.Doc
	resolved.Mode = ModeVerbatim
	byID := castByID(req.Cast)
	resolved.Cast = make([]CastMember, len(req.Doc.Cast))
	for i, member := range req.Doc.Cast {
		resolved.Cast[i] = member
		runtime, ok := byID[member.ID]
		if !ok {
			continue
		}
		resolved.Cast[i].DisplayName = runtime.DisplayName
		resolved.Cast[i].TTSProvider = runtime.Template.Provider
		resolved.Cast[i].TTSVoice = runtime.Template.Voice
		resolved.Cast[i].TTSTier = runtime.Template.Tier
		resolved.Cast[i].BrainProvider = runtime.BrainProvider
		resolved.Cast[i].BrainModel = runtime.BrainModel
	}
	return resolved
}

// absOrAsGiven resolves a path for the manifest, falling back to what the
// operator typed when the working directory cannot be read.
func absOrAsGiven(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
