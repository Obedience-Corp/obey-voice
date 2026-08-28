package scene

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixturePath = "../../testdata/scenes/two-voices.verbatim.yaml"

// validScript is the smallest document every rejection case mutates away from,
// so a failing case fails for the reason it names.
const validScript = `schema: obey-voice.scene.v1
id: coffee-counter
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
turns:
  - speaker: sam
    text: "You're late."
`

func TestSceneParseRejects(t *testing.T) {
	cases := map[string]string{
		"empty document": ``,
		"not yaml":       "\tschema: [",
		"wrong schema": `schema: obey-voice.render.v1
id: a-scene
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"unknown top-level key": validScript + "narrator: sam\n",
		"unknown cast key": `schema: obey-voice.scene.v1
id: a-scene
cast:
  - id: sam
    persona: samantha
    voice: af_heart
  - id: uncle
    persona: uncle-fu
turns: [{speaker: sam, text: hi}]`,
		"non-kebab id": `schema: obey-voice.scene.v1
id: Coffee_Counter
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"unsupported mode": `schema: obey-voice.scene.v1
id: a-scene
mode: improvise
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"unparsable pause": `schema: obey-voice.scene.v1
id: a-scene
pause: soon
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"negative pause": `schema: obey-voice.scene.v1
id: a-scene
pause: -400ms
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"one cast member": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: samantha}]
turns: [{speaker: sam, text: hi}]`,
		"duplicate cast id": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: samantha}, {id: sam, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"non-kebab persona id": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: Samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"unknown speaker": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: samantha, text: hi}]`,
		"blank turn text": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: "   "}]`,
		"verbatim without turns": `schema: obey-voice.scene.v1
id: a-scene
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]`,
		"generate without beats or opener": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  setting: A coffee counter.`,
		"generate without a scene block": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]`,
		"opener names a persona not a cast id": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  opener: {speaker: samantha, text: "You're late."}`,
		"blank opener text": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  beats: ["they argue"]
  opener: {speaker: sam, text: " "}`,
		"max_turns over the cap": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  beats: ["they argue"]
  max_turns: 49`,
		"too many turns": manyTurns(MaxTurns + 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}
}

func TestSceneParseAccepts(t *testing.T) {
	cases := map[string]string{
		"minimal verbatim": validScript,
		"empty mode is verbatim": `schema: obey-voice.scene.v1
id: a-scene
mode: ""
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]`,
		"generate with beats and no turns": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  setting: A coffee counter, morning rush.
  beats: ["uncle dodges", "sam does not buy it"]
  max_turns: 8
  stop_phrases: ["[END]"]`,
		"generate with only an opener": `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  opener: {speaker: sam, text: "You're late."}`,
		"three cast members are schema-legal": `schema: obey-voice.scene.v1
id: a-scene
cast:
  - {id: sam, persona: samantha}
  - {id: uncle, persona: uncle-fu}
  - {id: barista, persona: uncle-fu}
turns: [{speaker: barista, text: "Next."}]`,
		"turns at the cap": manyTurns(MaxTurns),
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(script)); err != nil {
				t.Fatalf("Parse(%s) = %v, want no error", name, err)
			}
		})
	}
}

// v1 records two voices. The schema stays N-cast ready, so the cast-size rule
// lives in ValidateV1 rather than in Validate.
func TestSceneValidateV1CastSize(t *testing.T) {
	cases := map[string]struct {
		script  string
		wantErr bool
	}{
		"two speakers": {validScript, false},
		"three speakers": {`schema: obey-voice.scene.v1
id: a-scene
cast:
  - {id: sam, persona: samantha}
  - {id: uncle, persona: uncle-fu}
  - {id: barista, persona: uncle-fu}
turns: [{speaker: barista, text: "Next."}]`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc, err := Parse([]byte(tc.script))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			err = doc.ValidateV1()
			if tc.wantErr && err == nil {
				t.Fatal("ValidateV1 accepted a cast v1 cannot record")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateV1 = %v, want no error", err)
			}
		})
	}
}

func TestSceneLoadFixture(t *testing.T) {
	doc, err := Load(context.Background(), fixturePath)
	if err != nil {
		t.Fatalf("Load(%s) = %v", fixturePath, err)
	}
	if err := doc.ValidateV1(); err != nil {
		t.Fatalf("the committed fixture must satisfy v1: %v", err)
	}
	if doc.ID != "coffee-counter" || doc.Title == "" {
		t.Errorf("fixture id/title = %q/%q", doc.ID, doc.Title)
	}
	if len(doc.Cast) != 2 {
		t.Errorf("fixture cast = %d, want 2", len(doc.Cast))
	}
	if len(doc.Turns) < 3 {
		t.Errorf("fixture turns = %d, want at least 3", len(doc.Turns))
	}
	for i, turn := range doc.Turns {
		if strings.TrimSpace(turn.Text) == "" {
			t.Errorf("fixture turns[%d] has no text", i)
		}
	}
}

func TestSceneLoadMissingFile(t *testing.T) {
	if _, err := Load(context.Background(), filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error for a missing script")
	}
}

// A cancelled caller must not be handed a document it no longer wants.
func TestSceneLoadCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, fixturePath); err == nil {
		t.Fatal("Load on a cancelled context returned no error")
	}
}

// manyTurns builds a document with n alternating turns.
func manyTurns(n int) string {
	var b strings.Builder
	b.WriteString("schema: obey-voice.scene.v1\nid: a-scene\n")
	b.WriteString("cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]\nturns:\n")
	for i := 0; i < n; i++ {
		speaker := "sam"
		if i%2 == 1 {
			speaker = "uncle"
		}
		b.WriteString("  - {speaker: " + speaker + ", text: line}\n")
	}
	return b.String()
}

// The fixture the docs point at has to exist where they say it does.
func TestSceneFixtureIsCommitted(t *testing.T) {
	if _, err := os.Stat(fixturePath); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
}
