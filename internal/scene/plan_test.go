package scene

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

// fakePersonas returns a loader over an in-memory persona store, so plan tests
// need nothing on disk and never read the operator's install.
func fakePersonas(profiles ...*persona.Profile) PersonaLoader {
	byID := map[string]*persona.Profile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	return func(id string) (*persona.Profile, error) {
		p, ok := byID[id]
		if !ok {
			return nil, errors.New("not found")
		}
		return p, nil
	}
}

func profile(id, display, provider, voice, tier string) *persona.Profile {
	return &persona.Profile{
		Schema:      persona.Schema,
		ID:          id,
		DisplayName: display,
		TTS:         persona.TTS{Provider: provider, Voice: voice, Tier: tier},
	}
}

func twoVoices() PersonaLoader {
	return fakePersonas(
		profile("samantha", "Samantha", "kokoro", "af_heart", ""),
		profile("uncle-fu", "Uncle Fu", "kokoro", "am_michael", ""),
	)
}

func mustParse(t *testing.T, script string) *Document {
	t.Helper()
	doc, err := Parse([]byte(script))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func TestSceneBuildPlanResolvesCast(t *testing.T) {
	doc := mustParse(t, validScript)
	plan, err := BuildPlan(context.Background(), doc, validOptions(), twoVoices())
	if err != nil {
		t.Fatalf("BuildPlan = %v", err)
	}
	if plan.Mode != ModeVerbatim || plan.Pause != DefaultPause || plan.Turns != 1 {
		t.Errorf("plan = %+v, want verbatim/%s/1 turn", plan, DefaultPause)
	}
	if plan.MaxTurns != DefaultMaxTurns {
		t.Errorf("plan max turns = %d, want %d", plan.MaxTurns, DefaultMaxTurns)
	}
	if plan.Script != "script.yaml" || plan.OutDir != "out/scene" {
		t.Errorf("plan script/out-dir = %q/%q", plan.Script, plan.OutDir)
	}
	if len(plan.Cast) != 2 {
		t.Fatalf("cast = %d, want 2", len(plan.Cast))
	}
	first := plan.Cast[0]
	if first.ID != "sam" || first.PersonaID != "samantha" || first.DisplayName != "Samantha" {
		t.Errorf("cast[0] = %+v", first)
	}
	if first.Mouth.Voice != "af_heart" {
		t.Errorf("cast[0] mouth = %s, want the persona's voice", first.Mouth)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("distinct voices warned anyway: %v", plan.Warnings)
	}
}

func TestSceneBuildPlanRejects(t *testing.T) {
	threeCast := `schema: obey-voice.scene.v1
id: a-scene
cast:
  - {id: sam, persona: samantha}
  - {id: uncle, persona: uncle-fu}
  - {id: barista, persona: samantha}
turns: [{speaker: sam, text: hi}]`
	generateOnly := `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  beats: ["they argue"]`

	cases := map[string]struct {
		doc  *Document
		opts Options
		load PersonaLoader
		want string
	}{
		"no document":         {nil, validOptions(), twoVoices(), "no script document"},
		"no persona loader":   {mustParse(t, validScript), validOptions(), nil, "no persona loader"},
		"cast v1 cannot cast": {mustParse(t, threeCast), validOptions(), twoVoices(), "v1 records exactly 2"},
		"persona not installed": {
			mustParse(t, validScript), validOptions(),
			fakePersonas(profile("samantha", "Samantha", "kokoro", "af_heart", "")),
			`cast "uncle": persona "uncle-fu"`,
		},
		"forcing verbatim onto a script with no turns": {
			mustParse(t, generateOnly),
			Options{Script: "s.yaml", OutDir: "out", Mode: "verbatim", ModeSet: true},
			twoVoices(),
			"needs turns[]",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPlan(context.Background(), tc.doc, tc.opts, tc.load)
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// One persona reading both parts is renderable but almost never intended, so it
// warns by default and fails when the caller says it must not happen.
func TestSceneBuildPlanSharedVoice(t *testing.T) {
	cases := map[string]PersonaLoader{
		"same provider and voice": fakePersonas(
			profile("samantha", "Samantha", "kokoro", "af_heart", ""),
			profile("uncle-fu", "Uncle Fu", "kokoro", "af_heart", ""),
		),
		"both inherit the app voice": fakePersonas(
			profile("samantha", "Samantha", "", "", ""),
			profile("uncle-fu", "Uncle Fu", "", "", ""),
		),
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustParse(t, validScript)

			plan, err := BuildPlan(context.Background(), doc, validOptions(), load)
			if err != nil {
				t.Fatalf("a shared voice must warn, not fail: %v", err)
			}
			if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "sam and uncle") {
				t.Fatalf("warnings = %v, want one naming both speakers", plan.Warnings)
			}

			strict := validOptions()
			strict.RequireDistinctVoices = true
			if _, err := BuildPlan(context.Background(), doc, strict, load); err == nil {
				t.Fatal("--require-distinct-voices accepted a shared voice")
			}
		})
	}
}

// A differing tier is a different mouth even when the voice name matches.
func TestSceneBuildPlanDistinctTiersDoNotWarn(t *testing.T) {
	load := fakePersonas(
		profile("samantha", "Samantha", "qwen3-tts", "Vivian", "0.6b"),
		profile("uncle-fu", "Uncle Fu", "qwen3-tts", "Vivian", "1.7b"),
	)
	plan, err := BuildPlan(context.Background(), mustParse(t, validScript), validOptions(), load)
	if err != nil {
		t.Fatalf("BuildPlan = %v", err)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", plan.Warnings)
	}
}

// Resolving the cast reads persona files, so a cancelled caller stops there.
func TestSceneResolveCastCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BuildPlan(ctx, mustParse(t, validScript), validOptions(), twoVoices()); err == nil {
		t.Fatal("BuildPlan on a cancelled context returned no error")
	}
}
