package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/scene"
)

const sceneFixture = "../../../testdata/scenes/two-voices.verbatim.yaml"

// scenePersonas is an in-memory persona store, so the command tests exercise
// the real plan path without reading the operator's install.
func scenePersonas(voices map[string]string) scene.PersonaLoader {
	return func(id string) (*persona.Profile, error) {
		voice, ok := voices[id]
		if !ok {
			return nil, errors.New("not found")
		}
		return &persona.Profile{
			Schema:      persona.Schema,
			ID:          id,
			DisplayName: id,
			TTS:         persona.TTS{Provider: "kokoro", Voice: voice},
		}, nil
	}
}

func distinctScenePersonas() scene.PersonaLoader {
	return scenePersonas(map[string]string{"samantha": "af_heart", "uncle-fu": "am_michael"})
}

// runScene executes the command against an injected persona store, so what is
// under test is the shipped plan runner rather than a stand-in.
func runScene(t *testing.T, load scene.PersonaLoader, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSceneCmd(func(cmd *cobra.Command, opts scene.Options) error {
		return runScenePlanWith(cmd, opts, load)
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// writeScene writes a script to a temp file and returns its path.
func writeScene(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

func TestSceneCmdRejectsInvalidInvocations(t *testing.T) {
	cases := map[string][]string{
		"no script":          {"--out-dir", "out/scene"},
		"no out-dir":         {sceneFixture},
		"two scripts":        {sceneFixture, sceneFixture, "--out-dir", "out/scene"},
		"bad mode":           {sceneFixture, "--out-dir", "out/scene", "--mode", "improvise"},
		"bad pause":          {sceneFixture, "--out-dir", "out/scene", "--pause", "soon"},
		"zero max-turns":     {sceneFixture, "--out-dir", "out/scene", "--max-turns", "0"},
		"max-turns over cap": {sceneFixture, "--out-dir", "out/scene", "--max-turns", "49"},
		"bad audio-format":   {sceneFixture, "--out-dir", "out/scene", "--audio-format", "flac"},
		"missing script file": {
			filepath.Join(t.TempDir(), "nope.yaml"), "--out-dir", "out/scene",
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := runScene(t, distinctScenePersonas(), args...); err == nil {
				t.Fatalf("expected an error for args %v", args)
			}
		})
	}
}

func TestSceneCmdPlan(t *testing.T) {
	out, _, err := runScene(t, distinctScenePersonas(), sceneFixture, "--out-dir", "out/coffee")
	if err != nil {
		t.Fatalf("scene plan error = %v", err)
	}
	for _, want := range []string{"Scene plan", "out/coffee", "verbatim", "400ms", "sam", "uncle", "af_heart", "no audio produced"} {
		if !contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}

// The document wins for a flag the operator left alone, and loses to one they
// passed.
func TestSceneCmdOverridePrecedence(t *testing.T) {
	script := writeScene(t, `schema: obey-voice.scene.v1
id: a-scene
pause: 250ms
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]
`)
	cases := map[string]struct {
		args      []string
		wantPause string
	}{
		"document pause": {[]string{script, "--out-dir", "out"}, "250ms"},
		"flag pause":     {[]string{script, "--out-dir", "out", "--pause", "1s"}, "1s"},
		"zero pause":     {[]string{script, "--out-dir", "out", "--pause", "0s"}, "0s"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, _, err := runScene(t, distinctScenePersonas(), tc.args...)
			if err != nil {
				t.Fatalf("scene plan error = %v", err)
			}
			if !contains(out, "pause:  "+tc.wantPause) {
				t.Errorf("plan pause is not %s:\n%s", tc.wantPause, out)
			}
		})
	}
}

// Generating dialogue is not in this build yet, in either spelling.
func TestSceneCmdGenerateIsNotInThisBuild(t *testing.T) {
	generateScript := writeScene(t, `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  beats: ["they argue about the bus"]
`)
	cases := map[string][]string{
		"--mode generate":        {sceneFixture, "--out-dir", "out", "--mode", "generate"},
		"a generate script":      {generateScript, "--out-dir", "out"},
		"--resolve-only":         {sceneFixture, "--out-dir", "out", "--resolve-only"},
		"--resolve-only on both": {generateScript, "--out-dir", "out", "--resolve-only"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := runScene(t, distinctScenePersonas(), args...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != "scene: generated mode is not in this build" {
				t.Fatalf("error = %q, want the not-in-this-build message", err)
			}
		})
	}
}

// The script's own problems have to surface before "not in this build", or a
// generate script can never be debugged against this binary.
func TestSceneCmdValidatesBeforeRefusingGenerate(t *testing.T) {
	broken := writeScene(t, `schema: obey-voice.scene.v1
id: a-scene
mode: generate
narrator: sam
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
scene:
  beats: ["they argue"]
`)
	_, _, err := runScene(t, distinctScenePersonas(), broken, "--out-dir", "out")
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if err.Error() == "scene: generated mode is not in this build" {
		t.Fatalf("the unknown key went unreported: %v", err)
	}
}

// Not overriding --mode must not turn a generate script into a verbatim one.
func TestSceneCmdOmittedModeDoesNotOverrideDocument(t *testing.T) {
	script := writeScene(t, `schema: obey-voice.scene.v1
id: a-scene
mode: generate
cast: [{id: sam, persona: samantha}, {id: uncle, persona: uncle-fu}]
turns: [{speaker: sam, text: hi}]
scene:
  beats: ["they argue"]
`)
	_, _, err := runScene(t, distinctScenePersonas(), script, "--out-dir", "out")
	if err == nil || err.Error() != "scene: generated mode is not in this build" {
		t.Fatalf("error = %v, want the document's generate mode to stand", err)
	}
}

// One persona reading both parts warns; --require-distinct-voices refuses it.
func TestSceneCmdSharedVoice(t *testing.T) {
	shared := scenePersonas(map[string]string{"samantha": "af_heart", "uncle-fu": "af_heart"})

	_, errOut, err := runScene(t, shared, sceneFixture, "--out-dir", "out")
	if err != nil {
		t.Fatalf("a shared voice must warn, not fail: %v", err)
	}
	if !contains(errOut, "warning") || !contains(errOut, "sam and uncle") {
		t.Errorf("stderr does not warn about the shared voice:\n%s", errOut)
	}

	_, _, err = runScene(t, shared, sceneFixture, "--out-dir", "out", "--require-distinct-voices")
	if err == nil {
		t.Fatal("--require-distinct-voices accepted a shared voice")
	}
}

// A cast persona that is not installed names the ones that are.
func TestSceneCmdMissingPersona(t *testing.T) {
	load := scenePersonas(map[string]string{"samantha": "af_heart"})
	_, _, err := runScene(t, load, sceneFixture, "--out-dir", "out")
	if err == nil {
		t.Fatal("expected an error for a missing persona")
	}
	if !contains(err.Error(), "uncle-fu") {
		t.Errorf("error does not name the missing persona: %v", err)
	}
}

// The plan runner writes no audio and creates no output directory: it is the
// runner the cgo-free integration binary ships.
func TestSceneCmdPlanWritesNothing(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	if _, _, err := runScene(t, distinctScenePersonas(), sceneFixture, "--out-dir", outDir); err != nil {
		t.Fatalf("scene plan error = %v", err)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatalf("plan runner created %s", outDir)
	}
}
