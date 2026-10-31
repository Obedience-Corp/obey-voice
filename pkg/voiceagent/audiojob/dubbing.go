package audiojob

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// TranslationReview contains immutable evidence, not paths into a generation
// cache. It survives project relocation together with the generated manifest.
type TranslationReview struct {
	Reviewed bool   `json:"reviewed"`
	Manifest string `json:"manifest_json"`
	Findings string `json:"findings_json"`
}

func (r Request) validateDubbing(c Config) error {
	if c.Managed == nil || r.Task != "dubbing" || r.Instruction != "" || strings.TrimSpace(r.Script) == "" || len(r.Script) > 1024 || !utf8.ValidString(r.Script) {
		return fmt.Errorf("v3 dubbing requires the managed pack, an approved script of 1–1024 UTF-8 bytes and no instruction")
	}
	if err := (TextTask{Task: "translation", Language: r.Language, TargetLanguage: r.TargetLanguage}).validate(); err != nil {
		return err
	}
	review := r.TranslationReview
	if review == nil || !review.Reviewed || len(review.Manifest)+len(review.Findings) > 256<<10 {
		return fmt.Errorf("dubbing requires an explicitly reviewed translation capsule of at most 256 KiB")
	}
	var m Manifest
	if err := json.Unmarshal([]byte(review.Manifest), &m); err != nil {
		return fmt.Errorf("reviewed translation manifest: %w", err)
	}
	// Input paths may change when a project moves, but no source identity or
	// PCM geometry may change between translation review and speech generation.
	original, current := m.Input, r.Input
	original.Path, current.Path = "", ""
	if m.Protocol != TextProtocol || m.Task != "translation" || m.Source != r.Source || original != current || m.Language != r.Language || m.TargetLanguage != r.TargetLanguage || m.Transport != "audio_to_text" || m.DurationPolicy != r.DurationPolicy {
		return fmt.Errorf("reviewed translation does not match the selected source, range, revision or languages")
	}
	if m.Runtime.Provider != r.Runtime.Provider || m.Runtime.Model != r.Runtime.Model || m.Runtime.Endpoint != r.Runtime.Endpoint || m.RuntimeVersion != c.Managed.Version || m.ModelDigest != c.Managed.Model.SHA256 || m.ProjectorDigest != c.Managed.Projector.SHA256 || m.RuntimeDigest != c.Managed.Executable.SHA256 {
		return fmt.Errorf("reviewed translation pack provenance does not match")
	}
	task := TextTask{Task: "translation", Language: r.Language, TargetLanguage: r.TargetLanguage}
	if m.PromptSHA256 != hashBytes([]byte(task.prompt())) || m.InstructionSHA256 != hashBytes(nil) || m.ChunkPolicy != ChunkPolicy || m.TextSampling == nil || *m.TextSampling != (TextSampling{Seed: 42, Temperature: 0, MaxTokens: MaxTextTokens, ContextTokens: ManagedContextTokens}) || m.Output.Kind != "json" || m.Output.Path != "findings.json" || m.Output.SampleCount != 0 || m.Output.Bytes != int64(len(review.Findings)) || m.Output.SHA256 != hashBytes([]byte(review.Findings)) {
		return fmt.Errorf("reviewed translation findings or prompt integrity check failed")
	}
	var findings struct {
		Schema         string       `json:"schema"`
		Task           string       `json:"task"`
		Language       string       `json:"language"`
		TargetLanguage string       `json:"target_language"`
		Windows        []TextWindow `json:"windows"`
	}
	if err := json.Unmarshal([]byte(review.Findings), &findings); err != nil {
		return fmt.Errorf("reviewed translation findings: %w", err)
	}
	// Bound planning by the already bounded review document before allocating.
	// A forged enormous sample_count must not turn validation into an OOM.
	if len(findings.Windows) == 0 || r.Input.SampleCount > int64(len(findings.Windows))*WindowSamples {
		return fmt.Errorf("reviewed translation has incomplete source coverage")
	}
	windows := textWindows(r.Input)
	if findings.Schema != FindingsSchema || findings.Task != "translation" || findings.Language != r.Language || findings.TargetLanguage != r.TargetLanguage || len(findings.Windows) != len(windows) || len(m.SourceWindows) != len(windows) {
		return fmt.Errorf("reviewed translation has incomplete source coverage")
	}
	for i, window := range windows {
		found := findings.Windows[i]
		if m.SourceWindows[i] != window || found.StartSample != window.StartSample || found.EndSample != window.EndSample || !HashValid(found.InputSHA256) || strings.TrimSpace(found.Text) == "" || !utf8.ValidString(found.Text) || found.Usage.PromptTokens < 1 || found.Usage.PromptTokens > ManagedContextTokens-MaxTextTokens || found.Usage.CompletionTokens < 1 || found.Usage.CompletionTokens > MaxTextTokens {
			return fmt.Errorf("reviewed translation window %d is invalid", i)
		}
	}
	return nil
}
