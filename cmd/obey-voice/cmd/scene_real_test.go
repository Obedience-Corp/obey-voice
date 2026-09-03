//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/scene"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const sceneGenerateFixture = "../../../testdata/scenes/two-voices.generate.yaml"

// sceneAppConfig is a stand-in install: none of these tests may read the
// operator's real config or personas.
func sceneAppConfig() *config.Config {
	return &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart", BrainProvider: "ollama"}
}

// sceneProfiles builds an in-memory persona store from id -> provider/voice/tier.
func sceneProfiles(specs map[string][3]string) scene.PersonaLoader {
	return func(id string) (*persona.Profile, error) {
		spec, ok := specs[id]
		if !ok {
			return nil, os.ErrNotExist
		}
		return &persona.Profile{
			Schema:      persona.Schema,
			ID:          id,
			DisplayName: id,
			TTS:         persona.TTS{Provider: spec[0], Voice: spec[1], Tier: spec[2]},
		}, nil
	}
}

// scriptedBrain is a stand-in persona brain for the command tests: it answers
// with numbered lines, or fails on the call it is told to.
type scriptedBrain struct {
	name   string
	calls  int
	failAt int
}

func (b *scriptedBrain) ThinkFull(_ context.Context, _ string, opts brain.StreamOptions) (string, error) {
	b.calls++
	if opts.ToolsEnabled {
		return "", errors.New("tools were enabled on a scene brain")
	}
	if b.failAt != 0 && b.calls >= b.failAt {
		return "", errors.New("ollama: connection refused")
	}
	return fmt.Sprintf("%s line %d", b.name, b.calls), nil
}

func (b *scriptedBrain) ClearHistory()              {}
func (b *scriptedBrain) LoadHistory(_ []brain.Turn) {}

// sceneBrains is a brain factory that counts constructions and can make one
// persona's brain fail.
type sceneBrains struct {
	made   int
	failAt map[string]int
}

func (f *sceneBrains) factory(cfg *config.Config) (scene.Brain, error) {
	f.made++
	return &scriptedBrain{name: cfg.AgentName, failAt: f.failAt[cfg.AgentName]}, nil
}

// runSceneReal drives the real runner up to the point where it would construct
// TTS. Every test here asserts on something that happens before that, so no
// model is ever loaded.
func runSceneReal(t *testing.T, load scene.PersonaLoader, args ...string) (string, string, error) {
	t.Helper()
	return runSceneRealWith(t, load, &sceneBrains{}, args...)
}

func runSceneRealWith(t *testing.T, load scene.PersonaLoader, brains *sceneBrains, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSceneCmd(func(cmd *cobra.Command, opts scene.Options) error {
		return runSceneWith(cmd, opts, nil, load, func() (*config.Config, error) { return sceneAppConfig(), nil }, brains.factory)
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func distinctSceneProfiles() scene.PersonaLoader {
	return sceneProfiles(map[string][3]string{
		"samantha": {"kokoro", "af_heart", ""},
		"uncle-fu": {"kokoro", "am_michael", ""},
	})
}

func sceneJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("summary is not JSON: %v\n%s", err, out)
	}
	return payload
}

// The encoder is resolved before a config is loaded, a persona is read, or a
// model is touched: a missing ffmpeg must not cost a Qwen worker start.
func TestSceneCmdEncoderPreflightRunsFirst(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	cmd := newSceneCmd(runSceneText)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{sceneFixture, "--out-dir", outDir, "--audio-format", "mp3", "--encoder", "obey-voice-no-such-encoder"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("an unavailable encoder was accepted")
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("the failed preflight created %s", outDir)
	}
}

// The effective mouths are what --require-distinct-voices judges. This is the
// pairing the declared persona fields cannot see: one persona names the app
// voice, the other inherits it.
func TestSceneCmdRequireDistinctVoicesUsesEffectiveMouths(t *testing.T) {
	inherited := sceneProfiles(map[string][3]string{
		"samantha": {"kokoro", "af_heart", ""},
		"uncle-fu": {"", "", ""},
	})

	_, errOut, err := runSceneReal(t, inherited, sceneFixture, "--out-dir", filepath.Join(t.TempDir(), "scene"), "--require-distinct-voices")
	if err == nil {
		t.Fatal("--require-distinct-voices accepted two personas that resolve to one voice")
	}
	if !contains(err.Error(), "sam and uncle") {
		t.Errorf("error does not name the colliding speakers: %v", err)
	}
	if contains(errOut, "warning") {
		t.Errorf("the refusal also warned:\n%s", errOut)
	}
}

// Without the flag the same collision warns and keeps going: one persona
// reading both parts is unusual, not forbidden.
func TestSceneCmdInheritedVoiceCollisionWarns(t *testing.T) {
	inherited := sceneProfiles(map[string][3]string{
		"samantha": {"kokoro", "af_heart", ""},
		"uncle-fu": {"", "", ""},
	})
	warnings := 0
	cmd := newSceneCmd(func(cmd *cobra.Command, opts scene.Options) error {
		doc, err := scene.Load(cmd.Context(), opts.Script)
		if err != nil {
			return err
		}
		plan, err := scene.BuildPlan(cmd.Context(), doc, opts, inherited)
		if err != nil {
			return err
		}
		cast, err := scene.BuildCast(sceneAppConfig(), plan.Cast)
		if err != nil {
			return err
		}
		warnings = len(scene.SharedVoices(cast))
		return reportSceneVoices(cmd, opts, cast)
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{sceneFixture, "--out-dir", filepath.Join(t.TempDir(), "scene")})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("a shared voice must warn, not fail: %v", err)
	}
	if warnings != 1 {
		t.Fatalf("SharedVoices found %d collision(s), want 1", warnings)
	}
	if stderr := errOut.String(); !contains(stderr, "warning") || !contains(stderr, "sam and uncle") {
		t.Errorf("stderr does not warn about the shared voice:\n%s", stderr)
	}
}

// Two Qwen tiers would mean two resident multi-gigabyte workers. It is refused
// before anything constructs one.
func TestSceneCmdRejectsMixedQwenTiers(t *testing.T) {
	mixed := sceneProfiles(map[string][3]string{
		"samantha": {"qwen3-tts", "Vivian", "0.6b"},
		"uncle-fu": {"qwen3-tts", "Uncle_Fu", "1.7b"},
	})
	_, _, err := runSceneReal(t, mixed, sceneFixture, "--out-dir", filepath.Join(t.TempDir(), "scene"))
	if err == nil {
		t.Fatal("a mixed-tier cast was accepted")
	}
	if !contains(err.Error(), "one qwen tier") {
		t.Errorf("error does not explain the tier rule: %v", err)
	}
}

// --resolve-only generates the lines with both brains and writes the resolved
// script, and constructs no TTS: the run returns before the pool, so no turns/
// directory and no manifest appear.
func TestSceneCmdResolveOnlyGeneratesWithoutTTS(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	brains := &sceneBrains{}

	out, _, err := runSceneRealWith(t, distinctSceneProfiles(), brains, sceneGenerateFixture, "--out-dir", outDir, "--resolve-only", "--json")
	if err != nil {
		t.Fatalf("resolve-only run failed: %v", err)
	}
	if brains.made != 2 {
		t.Errorf("constructed %d brains, want 2", brains.made)
	}
	payload := sceneJSON(t, out)
	for key, want := range map[string]any{"turns": 8.0, "completed": 8.0, "failed": 0.0, "conversation": "", "manifest": ""} {
		if payload[key] != want {
			t.Errorf("%s = %v, want %v", key, payload[key], want)
		}
	}
	if payload["script_resolved"] != filepath.Join(outDir, scene.ResolvedScriptFile) {
		t.Errorf("script_resolved = %v", payload["script_resolved"])
	}
	doc, err := scene.Load(t.Context(), filepath.Join(outDir, scene.ResolvedScriptFile))
	if err != nil {
		t.Fatalf("the resolved script does not load: %v", err)
	}
	if doc.Mode != scene.ModeVerbatim || len(doc.Turns) != 8 || doc.Turns[0].Text != "You're late." {
		t.Errorf("resolved script = %s with %d turns, first %q", doc.Mode, len(doc.Turns), doc.Turns[0].Text)
	}
	for _, name := range []string{scene.TurnsDir, scene.ManifestFile, scene.ConversationFile} {
		if _, statErr := os.Stat(filepath.Join(outDir, name)); !os.IsNotExist(statErr) {
			t.Errorf("a resolve-only run wrote %s", name)
		}
	}
}

// A brain that fails mid-scene stops the run before any mouth is constructed:
// the prefix is on disk, the --json counts say so, and the exit is non-zero.
func TestSceneCmdGenerateBrainErrorSkipsTTS(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	brains := &sceneBrains{failAt: map[string]int{"uncle-fu": 2}}

	out, _, err := runSceneRealWith(t, distinctSceneProfiles(), brains, sceneGenerateFixture, "--out-dir", outDir, "--json")
	if err == nil || !contains(err.Error(), "turn 4 (uncle)") {
		t.Fatalf("error = %v, want the failed turn named", err)
	}
	payload := sceneJSON(t, out)
	for key, want := range map[string]any{"turns": 3.0, "failed": 1.0, "conversation": ""} {
		if payload[key] != want {
			t.Errorf("%s = %v, want %v", key, payload[key], want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(outDir, scene.ResolvedScriptFile)); statErr != nil {
		t.Errorf("the prefix was not persisted: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, scene.TurnsDir)); !os.IsNotExist(statErr) {
		t.Error("a failed generate run reached TTS")
	}
}

// A verbatim script with --resolve-only writes the scripts, constructs no
// brain and no mouth, and reports its written lines.
func TestSceneCmdVerbatimResolveOnlyWritesScriptsOnly(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	brains := &sceneBrains{}

	out, _, err := runSceneRealWith(t, distinctSceneProfiles(), brains, sceneFixture, "--out-dir", outDir, "--resolve-only")
	if err != nil {
		t.Fatalf("resolve-only run failed: %v", err)
	}
	if brains.made != 0 {
		t.Errorf("a verbatim resolve constructed %d brain(s)", brains.made)
	}
	if !contains(out, "Resolved 4 turn(s) (0 generated)") {
		t.Errorf("summary:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, scene.ResolvedScriptFile)); statErr != nil {
		t.Errorf("no resolved script: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, scene.TurnsDir)); !os.IsNotExist(statErr) {
		t.Error("a resolve-only run reached TTS")
	}
}

// --mode generate on a script with no brief is refused at plan time, before
// the out-dir exists or a brain is constructed.
func TestSceneCmdGenerateNeedsABrief(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	brains := &sceneBrains{}

	_, _, err := runSceneRealWith(t, distinctSceneProfiles(), brains, sceneFixture, "--out-dir", outDir, "--mode", "generate")
	if err == nil || !contains(err.Error(), "beats or an opener") {
		t.Fatalf("error = %v, want the missing-brief refusal", err)
	}
	if brains.made != 0 {
		t.Errorf("constructed %d brain(s) for a refused script", brains.made)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Errorf("the refused run created %s", outDir)
	}
}

// The --json summary is the contract a calling script reads: the counts come
// out even when a turn failed, and the exit is still non-zero.
func TestSceneFinishSceneJSON(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "scene")
	result := scene.Result{
		Conversation: filepath.Join(outDir, scene.ConversationFile),
		Manifest: scene.RunManifest{
			Schema:     scene.RunSchema,
			SampleRate: 24000,
			DurationMS: 4210,
			Turns: []scene.RunTurn{
				{Status: scene.StatusComplete},
				{Status: scene.StatusSkipped},
				{Status: scene.StatusFailed},
			},
		},
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	renderErr := os.ErrInvalid
	if err := finishScene(cmd, scene.Options{OutDir: outDir, JSON: true}, result, nil, renderErr); err != renderErr {
		t.Fatalf("finishScene returned %v, wanted the render error preserved for the exit code", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("summary is not JSON: %v\n%s", err, out.String())
	}
	for key, want := range map[string]any{
		"turns": 3.0, "completed": 1.0, "skipped": 1.0, "failed": 1.0,
		"sample_rate": 24000.0, "duration_ms": 4210.0,
	} {
		if payload[key] != want {
			t.Errorf("%s = %v, want %v", key, payload[key], want)
		}
	}
	if payload["encoded"] == nil {
		t.Error("encoded is null, want an empty list")
	}
	for _, key := range []string{"output_dir", "conversation", "manifest", "script_resolved"} {
		if payload[key] == "" || payload[key] == nil {
			t.Errorf("%s is missing from the summary", key)
		}
	}
	// The manifest is persisted before the summary, so a failed run stays
	// resumable.
	if _, err := os.Stat(scene.ManifestPath(outDir)); err != nil {
		t.Errorf("no manifest was written: %v", err)
	}
}

// A provider that accepts a typed request but ignores the voice in it would
// read both parts identically while --require-distinct-voices still passed:
// the two personas declare different voices, so the mouths compare as distinct
// and only the pool rule can catch it. The cast is refused before any model
// loads.
func TestSceneCmdRejectsOneWorkerForBothVoices(t *testing.T) {
	single := sceneProfiles(map[string][3]string{
		"samantha": {"audio8", "voice-a", ""},
		"uncle-fu": {"audio8", "voice-b", ""},
	})
	outDir := filepath.Join(t.TempDir(), "scene")

	_, _, err := runSceneReal(t, single, sceneFixture, "--out-dir", outDir, "--require-distinct-voices")
	if err == nil {
		t.Fatal("a cast sharing one single-voice worker was accepted")
	}
	if !contains(err.Error(), "per-turn voice") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Errorf("the refused run created %s", outDir)
	}
}
