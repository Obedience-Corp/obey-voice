package scene

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RunSchema is the scene run manifest version. It is neither
// obey-voice.render.v1 nor a narration plan: a scene records who spoke, and
// when, on one shared timeline.
const RunSchema = "obey-voice.scene-run.v1"

// RendererVersion identifies output-affecting scene behavior (turn synthesis,
// concat, pause, clean rules). Bump it when a change makes the same script
// produce different audio, so --resume re-renders instead of mixing a run from
// two renderers.
const RendererVersion = "1"

// Turn statuses, matching render's vocabulary.
const (
	StatusComplete = "complete"
	StatusSkipped  = "skipped"
	StatusFailed   = "failed"
)

// Files a run writes under --out-dir.
const (
	ScriptFile         = "script.yaml"
	ResolvedScriptFile = "script.resolved.yaml"
	ConversationFile   = "conversation.wav"
	ManifestFile       = "manifest.json"
	TurnsDir           = "turns"
)

// RunCast records one speaker's effective stack, so a later captions or avatar
// step does not have to reopen persona files to learn who spoke.
type RunCast struct {
	ID            string `json:"id"`
	PersonaID     string `json:"persona_id"`
	DisplayName   string `json:"display_name"`
	TTSProvider   string `json:"tts_provider"`
	TTSVoice      string `json:"tts_voice"`
	TTSTier       string `json:"tts_tier"`
	BrainProvider string `json:"brain_provider"`
	BrainModel    string `json:"brain_model"`
}

// RunTurn is one spoken line as recorded. Speaker, Text, WAV, StartMS and
// DurationMS are the fields the video step is entitled to: the clocks are on
// the combined conversation.wav timeline, not on the turn's own WAV.
type RunTurn struct {
	Index       int    `json:"index"`
	ID          string `json:"id"`
	Speaker     string `json:"speaker"`
	PersonaID   string `json:"persona_id"`
	DisplayName string `json:"display_name"`
	Text        string `json:"text"`
	TextSHA256  string `json:"text_sha256"`
	WAV         string `json:"wav"`
	StartMS     int64  `json:"start_ms"`
	DurationMS  int64  `json:"duration_ms"`
	TTSProvider string `json:"tts_provider"`
	TTSVoice    string `json:"tts_voice"`
	SampleRate  int    `json:"sample_rate"`
	Status      string `json:"status"`
	ResumeKey   string `json:"resume_key,omitempty"`
}

// RunManifest is the inspectable record of one scene, written as manifest.json.
type RunManifest struct {
	Schema         string    `json:"schema"`
	CreatedAt      string    `json:"created_at,omitempty"`
	Source         string    `json:"source"`
	SourceSHA256   string    `json:"source_sha256,omitempty"`
	Title          string    `json:"title,omitempty"`
	Mode           Mode      `json:"mode"`
	PauseMS        int64     `json:"pause_ms"`
	SampleRate     int       `json:"sample_rate"`
	Conversation   string    `json:"conversation"`
	ScriptResolved string    `json:"script_resolved,omitempty"`
	DurationMS     int64     `json:"duration_ms"`
	Cast           []RunCast `json:"cast"`
	Turns          []RunTurn `json:"turns"`
}

// Counts returns the number of complete, skipped, and failed turns.
func (m RunManifest) Counts() (complete, skipped, failed int) {
	for _, turn := range m.Turns {
		switch turn.Status {
		case StatusComplete:
			complete++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}
	return
}

// ManifestPath is where a run's manifest lives under an output directory.
func ManifestPath(outDir string) string {
	return filepath.Join(outDir, ManifestFile)
}

// WriteRunManifest writes m to path as indented JSON via a temp file and a
// rename, so an interrupted write cannot leave a half-parsed manifest that a
// later --resume would read as authoritative.
func WriteRunManifest(path string, m RunManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("scene: manifest: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// LoadRunManifest reads a prior manifest for resume decisions. A missing,
// empty, or unreadable manifest yields ok=false, and resume then treats every
// turn as new rather than guessing.
func LoadRunManifest(path string) (RunManifest, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunManifest{}, false
	}
	var m RunManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return RunManifest{}, false
	}
	return m, true
}

// writeFileAtomic writes data to path through a temp file in the same
// directory, creating parents.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("scene: writing %s: %w", path, err)
	}
	committed = true
	return nil
}

// turnResumeKey is the fingerprint of one rendered turn. It folds in everything
// that changes the audio: the renderer, who is speaking, the mouth they speak
// with (including the Qwen fields that reshape a voice without renaming it),
// the line itself, and where it lands. Fields are domain-separated so no two
// field boundaries can collide.
func turnResumeKey(member CastRuntime, text, output string) string {
	t := member.Template
	h := sha256.New()
	for _, field := range []string{
		"rv=" + RendererVersion,
		"persona=" + member.PersonaID,
		"provider=" + t.Provider,
		"tier=" + t.Tier,
		"voice=" + t.Voice,
		"language=" + t.Language,
		"mode=" + t.Mode,
		"instruction=" + t.Instruction,
		"ref-audio=" + t.ReferenceAudio,
		"ref-text=" + t.ReferenceTranscript,
		"out=" + output,
		"text=" + textHash(text),
	} {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resumable reports whether a prior turn lets us skip re-synthesizing: it must
// match the recomputed key, still have its WAV on disk, and not be a failed
// turn. Failed turns stay visible and are always retried.
func resumable(prior RunTurn, key, wavPath string) bool {
	return prior.ResumeKey != "" && prior.ResumeKey == key && prior.Status != StatusFailed && pathExists(wavPath)
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
