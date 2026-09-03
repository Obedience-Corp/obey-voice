// Package scene is the batch scripted-conversation runtime: it turns an
// obey-voice.scene.v1 script into a two-persona recording, separately from the
// live voice pipeline. Everything here is cgo-free — synthesis, WAV writing and
// resampling are injected by the command layer — so the same command
// definition compiles into the integration binary.
package scene

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Schema identifies the scene script document version.
const Schema = "obey-voice.scene.v1"

// Mode selects where the spoken lines come from.
type Mode string

const (
	// ModeVerbatim speaks the turns[] the author wrote. It is the default for
	// an empty mode on either the document or the CLI.
	ModeVerbatim Mode = "verbatim"
	// ModeGenerate fills turns[] from the cast's brains before speaking.
	ModeGenerate Mode = "generate"
)

const (
	// DefaultPause is the silence between turns when neither the CLI nor the
	// document asks for one.
	DefaultPause = 400 * time.Millisecond
	// DefaultMaxTurns bounds a generated conversation by default.
	DefaultMaxTurns = 24
	// MaxTurns is the hard cap on turns in either mode: a scene is short-form,
	// not an audiobook.
	MaxTurns = 48
	// castSize is how many speakers v1 records. The schema allows more so an
	// N-cast script keeps validating once the renderer grows into it.
	castSize = 2
)

// idPattern is the kebab-case id shape shared with persona ids.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Document is one obey-voice.scene.v1 script: who speaks, and either the lines
// they speak (verbatim) or the beats they improvise from (generate).
//
// The json tags mirror the yaml paths so the resolved script and the run
// manifest speak the same snake_case as the authored document.
type Document struct {
	Schema string       `yaml:"schema" json:"schema"`
	ID     string       `yaml:"id" json:"id"`
	Title  string       `yaml:"title,omitempty" json:"title,omitempty"`
	Mode   Mode         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Pause  string       `yaml:"pause,omitempty" json:"pause,omitempty"`
	Cast   []CastMember `yaml:"cast" json:"cast"`
	Turns  []Turn       `yaml:"turns,omitempty" json:"turns,omitempty"`
	Scene  *SceneBlock  `yaml:"scene,omitempty" json:"scene,omitempty"`

	// GenerateFingerprint is output-only: the resolved script of a generated
	// run records which brief its lines came from, so --resume can tell a
	// prefix worth keeping from one written for a different scene. Authored
	// documents leave it empty; the loader ignores it.
	GenerateFingerprint string `yaml:"generate_fingerprint,omitempty" json:"generate_fingerprint,omitempty"`
}

// CastMember binds a scene-local speaker id to a persona profile. Turns name
// the cast id, not the persona id, so a recast does not rewrite every line.
//
// The remaining fields are output-only: the resolved script records each
// speaker's effective stack so a run that never reaches the manifest (a
// --resolve-only run, or one stopped by a brain error) still says which mouth
// and which model stood behind every line. The loader ignores them.
type CastMember struct {
	ID      string `yaml:"id" json:"id"`
	Persona string `yaml:"persona" json:"persona"`

	DisplayName   string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	TTSProvider   string `yaml:"tts_provider,omitempty" json:"tts_provider,omitempty"`
	TTSVoice      string `yaml:"tts_voice,omitempty" json:"tts_voice,omitempty"`
	TTSTier       string `yaml:"tts_tier,omitempty" json:"tts_tier,omitempty"`
	BrainProvider string `yaml:"brain_provider,omitempty" json:"brain_provider,omitempty"`
	BrainModel    string `yaml:"brain_model,omitempty" json:"brain_model,omitempty"`
}

// Turn is one spoken line.
type Turn struct {
	Speaker string `yaml:"speaker" json:"speaker"`
	Text    string `yaml:"text" json:"text"`
}

// SceneBlock is the improvisation brief. It is required for generate mode and
// preserved but unused in verbatim mode.
type SceneBlock struct {
	Setting     string   `yaml:"setting,omitempty" json:"setting,omitempty"`
	Beats       []string `yaml:"beats,omitempty" json:"beats,omitempty"`
	Opener      *Opener  `yaml:"opener,omitempty" json:"opener,omitempty"`
	MaxTurns    int      `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
	StopPhrases []string `yaml:"stop_phrases,omitempty" json:"stop_phrases,omitempty"`
}

// Opener seeds a generated conversation with a written first line, so the
// opening speaker does not have to invent one.
type Opener struct {
	Speaker string `yaml:"speaker,omitempty" json:"speaker,omitempty"`
	Text    string `yaml:"text" json:"text"`
}

// Load reads and validates a script. It takes a context because it touches the
// filesystem, and returns before the read when the caller has already given up.
func Load(ctx context.Context, path string) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("scene: reading script %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scene: reading script: %w", err)
	}
	return Parse(data)
}

// Parse strictly decodes a script document, rejecting unknown keys, and
// validates it. Unknown keys are a hard failure so a typo surfaces as an error
// instead of a silently ignored setting.
func Parse(data []byte) (*Document, error) {
	d, err := decode(data)
	if err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

// decode is the strict YAML half of Parse, without validation, for a reader
// that has its own rules about what the document must contain.
func decode(data []byte) (*Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Document
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("scene: parsing script: document is empty")
		}
		return nil, fmt.Errorf("scene: parsing script: %w", err)
	}
	return &d, nil
}

// Validate checks the document against the schema rules that need no
// filesystem, TTS or brain: shape, ids, references and caps.
func (d *Document) Validate() error {
	if d.Schema != Schema {
		return fmt.Errorf("scene: schema %q: want %q", d.Schema, Schema)
	}
	if err := validateID("id", d.ID); err != nil {
		return err
	}
	mode, err := parseMode("mode", string(d.Mode))
	if err != nil {
		return err
	}
	if _, err := parsePause("pause", d.Pause); err != nil {
		return err
	}
	speakers, err := d.validateCast()
	if err != nil {
		return err
	}
	if err := d.validateTurns(mode, speakers); err != nil {
		return err
	}
	return d.validateSceneBlock(mode, speakers)
}

// ValidateV1 adds the rule this renderer cannot yet relax on top of Validate:
// exactly two speakers.
func (d *Document) ValidateV1() error {
	if err := d.Validate(); err != nil {
		return err
	}
	if len(d.Cast) != castSize {
		return fmt.Errorf("scene: cast has %d members: v1 records exactly %d", len(d.Cast), castSize)
	}
	return nil
}

// ValidateFor re-checks the mode-dependent rules against the mode a run will
// actually use, which the --mode flag can make differ from the document's own.
// A verbatim script forced to generate still needs a brief; a generate script
// forced to verbatim still needs lines.
func (d *Document) ValidateFor(mode Mode) error {
	speakers, err := d.validateCast()
	if err != nil {
		return err
	}
	if err := d.validateTurns(mode, speakers); err != nil {
		return err
	}
	return d.validateSceneBlock(mode, speakers)
}

// validateCast checks cast shape and returns the set of legal speaker ids.
func (d *Document) validateCast() (map[string]bool, error) {
	if len(d.Cast) < castSize {
		return nil, fmt.Errorf("scene: cast has %d member(s): a scene needs at least %d", len(d.Cast), castSize)
	}
	speakers := make(map[string]bool, len(d.Cast))
	for i, member := range d.Cast {
		if err := validateID(fmt.Sprintf("cast[%d].id", i), member.ID); err != nil {
			return nil, err
		}
		if speakers[member.ID] {
			return nil, fmt.Errorf("scene: cast[%d].id %q: duplicate speaker id", i, member.ID)
		}
		if err := validateID(fmt.Sprintf("cast[%d].persona", i), member.Persona); err != nil {
			return nil, err
		}
		speakers[member.ID] = true
	}
	return speakers, nil
}

// validateTurns checks every written line, in either mode: a generate script
// may still carry turns (an opener, or a hand-edited prefix).
func (d *Document) validateTurns(mode Mode, speakers map[string]bool) error {
	if len(d.Turns) > MaxTurns {
		return fmt.Errorf("scene: turns has %d entries: at most %d", len(d.Turns), MaxTurns)
	}
	for i, turn := range d.Turns {
		if !speakers[turn.Speaker] {
			return fmt.Errorf("scene: turns[%d].speaker %q: not a cast id", i, turn.Speaker)
		}
		if strings.TrimSpace(turn.Text) == "" {
			return fmt.Errorf("scene: turns[%d].text: empty", i)
		}
	}
	if mode != ModeGenerate && len(d.Turns) == 0 {
		return fmt.Errorf("scene: turns: verbatim mode needs at least one turn")
	}
	return nil
}

// validateSceneBlock checks the improvisation brief. Generate mode needs
// something to improvise from; verbatim mode keeps whatever is there.
func (d *Document) validateSceneBlock(mode Mode, speakers map[string]bool) error {
	block := d.Scene
	if block == nil {
		if mode == ModeGenerate {
			return fmt.Errorf("scene: scene: generate mode needs beats or an opener")
		}
		return nil
	}
	if block.MaxTurns != 0 && (block.MaxTurns < 1 || block.MaxTurns > MaxTurns) {
		return fmt.Errorf("scene: scene.max_turns %d: must be between 1 and %d", block.MaxTurns, MaxTurns)
	}
	if block.Opener != nil {
		if block.Opener.Speaker != "" && !speakers[block.Opener.Speaker] {
			return fmt.Errorf("scene: scene.opener.speaker %q: not a cast id", block.Opener.Speaker)
		}
		if strings.TrimSpace(block.Opener.Text) == "" {
			return fmt.Errorf("scene: scene.opener.text: empty")
		}
	}
	if mode == ModeGenerate && len(block.Beats) == 0 && block.Opener == nil {
		return fmt.Errorf("scene: scene: generate mode needs beats or an opener")
	}
	return nil
}

// validateID enforces lowercase kebab-case ids, the shape persona ids already
// use, so a cast id is always a safe output filename component.
func validateID(field, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("scene: %s: empty", field)
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("scene: %s %q: must be lowercase kebab-case [a-z0-9-]+", field, id)
	}
	return nil
}

// parseMode resolves a mode string from either the document or the CLI. Empty
// means verbatim, which is what an unset --mode and an absent key both yield.
func parseMode(field, raw string) (Mode, error) {
	switch mode := Mode(strings.ToLower(strings.TrimSpace(raw))); mode {
	case "", ModeVerbatim:
		return ModeVerbatim, nil
	case ModeGenerate:
		return ModeGenerate, nil
	default:
		return "", fmt.Errorf("scene: unsupported %s %q (try verbatim or generate)", field, raw)
	}
}

// parsePause parses an optional Go duration. Empty means "not set" and yields
// zero; the caller decides whether that falls back to DefaultPause.
func parsePause(field, raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("scene: invalid %s %q: %w", field, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("scene: %s must be >= 0, got %s", field, raw)
	}
	return d, nil
}
