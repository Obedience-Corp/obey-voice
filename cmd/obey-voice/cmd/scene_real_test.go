//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/scene"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

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

// runSceneReal drives the real runner up to the point where it would construct
// TTS. Every test here asserts on a refusal that happens before that, so no
// model is ever loaded.
func runSceneReal(t *testing.T, load scene.PersonaLoader, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSceneCmd(func(cmd *cobra.Command, opts scene.Options) error {
		return runSceneWith(cmd, opts, nil, load, func() (*config.Config, error) { return sceneAppConfig(), nil })
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
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

// Generating dialogue is refused before any TTS is constructed, in either
// spelling, on the recording binary too.
func TestSceneCmdRealRunnerRefusesGenerate(t *testing.T) {
	distinct := sceneProfiles(map[string][3]string{
		"samantha": {"kokoro", "af_heart", ""},
		"uncle-fu": {"kokoro", "am_michael", ""},
	})
	outDir := filepath.Join(t.TempDir(), "scene")
	for name, args := range map[string][]string{
		"--mode generate": {sceneFixture, "--out-dir", outDir, "--mode", "generate"},
		"--resolve-only":  {sceneFixture, "--out-dir", outDir, "--resolve-only"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := runSceneReal(t, distinct, args...)
			if err == nil || err.Error() != "scene: generated mode is not in this build" {
				t.Fatalf("error = %v, want the not-in-this-build message", err)
			}
			if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
				t.Errorf("a refused generate run created %s", outDir)
			}
		})
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
