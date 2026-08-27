package scene

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

// PersonaLoader resolves a cast member's persona profile by id. Production
// passes a wrapper around persona.Load; tests inject a fake so validation needs
// no personas on disk.
type PersonaLoader func(id string) (*persona.Profile, error)

// Mouth is a cast member's TTS identity: the engine that speaks their lines and
// the voice it speaks them in. Empty fields inherit the app defaults, so two
// personas that declare nothing share one mouth.
type Mouth struct {
	Provider string
	Voice    string
	Tier     string
}

// String renders a mouth for a plan line, naming inherited fields rather than
// printing a blank so a collision on the app default is readable.
func (m Mouth) String() string {
	parts := []string{orAppDefault(m.Provider), orAppDefault(m.Voice)}
	if m.Tier != "" {
		parts = append(parts, m.Tier)
	}
	return strings.Join(parts, "/")
}

func orAppDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(app default)"
	}
	return value
}

// ResolvedCast is one cast member with their persona profile loaded.
type ResolvedCast struct {
	ID          string // scene-local speaker id
	PersonaID   string
	DisplayName string
	Mouth       Mouth
}

// ResolveCast loads every cast member's persona through load. It is the only
// part of validation that reaches the filesystem, so it takes a context and
// stops between members once the caller has given up.
func (d *Document) ResolveCast(ctx context.Context, load PersonaLoader) ([]ResolvedCast, error) {
	if load == nil {
		return nil, fmt.Errorf("scene: no persona loader")
	}
	resolved := make([]ResolvedCast, 0, len(d.Cast))
	for _, member := range d.Cast {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scene: resolving cast: %w", err)
		}
		profile, err := load(member.Persona)
		if err != nil {
			return nil, fmt.Errorf("scene: cast %q: persona %q: %w", member.ID, member.Persona, err)
		}
		resolved = append(resolved, ResolvedCast{
			ID:          member.ID,
			PersonaID:   profile.ID,
			DisplayName: profile.DisplayName,
			Mouth: Mouth{
				Provider: profile.TTS.Provider,
				Voice:    profile.TTS.Voice,
				Tier:     profile.TTS.Tier,
			},
		})
	}
	return resolved, nil
}

// Plan is the resolved summary of a scene invocation: the effective mode,
// pause and cast, with every persona already loaded. Building one performs no
// synthesis, so both the synthesizing and the cgo-free runner start here.
type Plan struct {
	Script   string
	OutDir   string
	Title    string
	Mode     Mode
	Pause    time.Duration
	MaxTurns int
	Cast     []ResolvedCast
	Turns    int
	Warnings []string
}

// BuildPlan validates doc for v1, resolves its cast, and folds the CLI
// overrides in. Cast members that would speak with the same mouth are a warning
// (two voices are the point of a scene, but one persona reading both parts is a
// legitimate thing to render); RequireDistinctVoices promotes it to an error.
func BuildPlan(ctx context.Context, doc *Document, opts Options, load PersonaLoader) (*Plan, error) {
	if doc == nil {
		return nil, fmt.Errorf("scene: no script document")
	}
	if err := doc.ValidateV1(); err != nil {
		return nil, err
	}
	cast, err := doc.ResolveCast(ctx, load)
	if err != nil {
		return nil, err
	}

	mode := opts.EffectiveMode(doc)
	if mode == ModeVerbatim && len(doc.Turns) == 0 {
		return nil, fmt.Errorf("scene: --mode verbatim needs turns[]: the script has none")
	}

	warnings := sharedMouths(cast)
	if len(warnings) > 0 && opts.RequireDistinctVoices {
		return nil, fmt.Errorf("scene: --require-distinct-voices: %s", warnings[0])
	}

	return &Plan{
		Script:   opts.Script,
		OutDir:   opts.OutDir,
		Title:    doc.Title,
		Mode:     mode,
		Pause:    opts.EffectivePause(doc),
		MaxTurns: opts.EffectiveMaxTurns(doc),
		Cast:     cast,
		Turns:    len(doc.Turns),
		Warnings: warnings,
	}, nil
}

// sharedMouths reports each group of cast members that would speak with the
// same voice, in cast order. Personas that both inherit the app voice collide
// on it, which is why an empty Mouth compares like any other value.
func sharedMouths(cast []ResolvedCast) []string {
	groups := map[Mouth][]string{}
	for _, member := range cast {
		groups[member.Mouth] = append(groups[member.Mouth], member.ID)
	}
	var warnings []string
	for mouth, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s speak with the same voice %s", strings.Join(ids, " and "), mouth))
	}
	sort.Strings(warnings)
	return warnings
}
