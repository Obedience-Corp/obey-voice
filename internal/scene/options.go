package scene

import (
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/render/encoder"
)

// Options describes one `obey-voice scene` invocation.
//
// Mode, Pause and MaxTurns are overrides rather than settings: the script
// document carries the authored values, and a flag only displaces one when the
// operator actually passed it. The command layer records that in the matching
// *Set field, so "the flag defaulted to empty" and "the operator asked for
// empty" stay distinguishable.
type Options struct {
	Script string // positional script path
	OutDir string // required output directory

	Mode        string
	ModeSet     bool
	Pause       string
	PauseSet    bool
	MaxTurns    int
	MaxTurnsSet bool

	JSON        bool   // print a machine-readable summary
	Resume      bool   // keep turns whose audio is already rendered and unchanged
	Overwrite   bool   // replace existing outputs
	AudioFormat string // optionally encode conversation.wav (mp3|m4a|m4b|aac|opus)
	EncoderBin  string // external encoder binary (default: ffmpeg)

	ResolveOnly           bool // generate the dialogue, write it, synthesize nothing
	RequireDistinctVoices bool // fail instead of warn when both personas share a voice
}

// Validate checks the invocation before any script is read. It is cgo-free and
// touches neither the network nor the filesystem, so a bad flag fails fast.
func (o Options) Validate() error {
	if strings.TrimSpace(o.Script) == "" {
		return fmt.Errorf("scene: provide a script path")
	}
	if strings.TrimSpace(o.OutDir) == "" {
		return fmt.Errorf("scene: provide --out-dir DIR")
	}
	if o.ModeSet {
		if _, err := parseMode("--mode", o.Mode); err != nil {
			return err
		}
	}
	if o.PauseSet {
		if _, err := parsePause("--pause", o.Pause); err != nil {
			return err
		}
	}
	if o.MaxTurnsSet && (o.MaxTurns < 1 || o.MaxTurns > MaxTurns) {
		return fmt.Errorf("scene: --max-turns must be between 1 and %d, got %d", MaxTurns, o.MaxTurns)
	}
	if !encoder.Supported(o.AudioFormat) {
		return fmt.Errorf("scene: unsupported --audio-format %q (try one of: mp3, m4a, m4b, aac, opus)", o.AudioFormat)
	}
	return nil
}

// EffectiveMode returns the mode this run records in: the flag when the
// operator set it, else the document's, else verbatim. Both sides were already
// validated, so an unparsable value cannot reach here.
func (o Options) EffectiveMode(doc *Document) Mode {
	raw := ""
	switch {
	case o.ModeSet:
		raw = o.Mode
	case doc != nil:
		raw = string(doc.Mode)
	}
	mode, _ := parseMode("mode", raw)
	return mode
}

// EffectivePause returns the silence between turns. An explicit zero on either
// side means no silence; only an absent value falls back to DefaultPause.
func (o Options) EffectivePause(doc *Document) time.Duration {
	switch {
	case o.PauseSet:
		pause, _ := parsePause("--pause", o.Pause)
		return pause
	case doc != nil && strings.TrimSpace(doc.Pause) != "":
		pause, _ := parsePause("pause", doc.Pause)
		return pause
	}
	return DefaultPause
}

// EffectiveMaxTurns returns the cap on a generated conversation.
func (o Options) EffectiveMaxTurns(doc *Document) int {
	switch {
	case o.MaxTurnsSet:
		return o.MaxTurns
	case doc != nil && doc.Scene != nil && doc.Scene.MaxTurns > 0:
		return doc.Scene.MaxTurns
	}
	return DefaultMaxTurns
}
