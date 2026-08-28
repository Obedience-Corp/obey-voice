//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/render"
	"github.com/Obedience-Corp/obey-voice/internal/render/encoder"
	"github.com/Obedience-Corp/obey-voice/internal/scene"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// runSceneText is the real scene runner: two personas, two mouths, one
// conversation WAV. It constructs TTS and nothing else — no STT, no brain, no
// microphone, no playback.
func runSceneText(cmd *cobra.Command, opts scene.Options) error {
	// Preflight the optional encoder BEFORE anything loads a model, exactly as
	// render does: a missing ffmpeg must not cost a Qwen worker start.
	enc, err := render.ResolveEncoder(cmd.Context(), render.Options{
		AudioFormat: opts.AudioFormat,
		EncoderBin:  opts.EncoderBin,
	}, exec.LookPath)
	if err != nil {
		// The encoder stack is render's; the command that failed is this one.
		return fmt.Errorf("scene: %w", err)
	}
	return runSceneWith(cmd, opts, enc, loadScenePersona, config.Load)
}

// runSceneWith is runSceneText with the encoder, persona store and config
// loader injected, so the flow around the TTS pool is testable.
func runSceneWith(cmd *cobra.Command, opts scene.Options, enc encoder.Encoder, loadPersona scene.PersonaLoader, loadConfig func() (*config.Config, error)) error {
	ctx := cmd.Context()
	doc, err := scene.Load(ctx, opts.Script)
	if err != nil {
		return err
	}
	plan, err := scene.BuildPlan(ctx, doc, opts, loadPersona)
	if err != nil {
		return err
	}
	if plan.Mode == scene.ModeGenerate || opts.ResolveOnly {
		return errSceneGenerateUnsupported
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cast, err := scene.BuildCast(cfg, plan.Cast)
	if err != nil {
		return err
	}
	if err := scene.ValidatePool(cast); err != nil {
		return err
	}
	if err := reportSceneVoices(cmd, opts, cast); err != nil {
		return err
	}

	synth, cleanup, err := newSceneSynth(ctx, cast)
	if err != nil {
		return err
	}
	defer cleanup()

	renderer := scene.Renderer{
		Synthesize: synth,
		WriteWAV:   audio.WriteWAVFloat32,
		ReadWAV:    audio.ReadWAVFloat32,
		Resample:   scene.ResampleLinear,
		Progress:   sceneProgress(cmd, opts),
	}
	result, renderErr := renderer.Render(ctx, scene.Request{Doc: doc, Plan: plan, Cast: cast, Options: opts})
	return finishScene(cmd, opts, result, enc, renderErr)
}

// reportSceneVoices applies the same-voice policy to the *effective* mouths.
// A persona that names a voice and one that inherits the same app default are
// one mouth reading both parts, and neither persona document says so.
func reportSceneVoices(cmd *cobra.Command, opts scene.Options, cast []scene.CastRuntime) error {
	warnings := scene.SharedVoices(cast)
	if len(warnings) == 0 {
		return nil
	}
	if opts.RequireDistinctVoices {
		return fmt.Errorf("scene: --require-distinct-voices: %s", warnings[0])
	}
	for _, warning := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", warning)
	}
	return nil
}

// sceneProgress reports finished turns on stderr for humans watching a long
// run. A --json run stays machine-clean.
func sceneProgress(cmd *cobra.Command, opts scene.Options) func(string, ...any) {
	if opts.JSON {
		return nil
	}
	errOut := cmd.ErrOrStderr()
	return func(format string, args ...any) {
		fmt.Fprintf(errOut, "  "+format+"\n", args...)
	}
}

// newSceneSynth constructs one TTS provider per pool key and returns a
// synthesizer that routes every turn to its cast member's worker.
//
// Two Kokoro voices, or two Qwen voices on one tier, share a single provider:
// the turn's template carries what differs. Assets are ensured per pool key on
// that member's applied config, because NeedTTS follows the provider on the
// config it is given — one ensure on the app config leaves a mixed
// Kokoro + Qwen cast half-installed.
//
// A provider that cannot take a typed request fails the run here, once, rather
// than every turn in turn — the rest of this command is deliberately
// fail-before-work. Whether a provider that *accepts* one actually honours the
// voice is a separate question, settled by ValidatePool before we get here.
func newSceneSynth(ctx context.Context, cast []scene.CastRuntime) (scene.Synthesizer, func(), error) {
	pool := map[scene.PoolKey]tts.RequestProvider{}
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	for _, member := range scene.PoolPlan(cast) {
		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("scene: preparing %s: %w", member.Key, err)
		}
		if err := config.EnsureRuntimeAssets(ctx, member.Cast.Config, config.AssetRequest{NeedTTS: true}, nil); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("scene: %s assets: %w", member.Key, err)
		}
		provider, done, err := tts.NewProvider(member.Cast.Config)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("scene: init %s: %w", member.Key, err)
		}
		if done != nil {
			cleanups = append(cleanups, done)
		}
		rp, ok := provider.(tts.RequestProvider)
		if !ok {
			cleanup()
			return nil, nil, fmt.Errorf("scene: %s does not accept a per-turn voice", member.Key)
		}
		pool[member.Key] = rp
	}

	synth := func(ctx context.Context, tmpl scene.SynthTemplate, text string) ([]float32, int, error) {
		key := scene.PoolKeyFor(tmpl)
		provider, ok := pool[key]
		if !ok {
			return nil, 0, fmt.Errorf("scene: no TTS provider for %s", key)
		}
		return synthesizeSceneTurn(ctx, provider, tmpl, text)
	}
	return synth, cleanup, nil
}

// synthesizeSceneTurn sends the turn's own template rather than the settings
// the provider was constructed with. Reusing render's adapter here would bind
// the constructing persona's voice over every request, which is right for a
// one-voice document and wrong for a conversation.
func synthesizeSceneTurn(ctx context.Context, provider tts.RequestProvider, tmpl scene.SynthTemplate, text string) ([]float32, int, error) {
	result, err := provider.SynthesizeRequest(ctx, tts.SynthesisRequest{
		Text:                text,
		Voice:               tmpl.Voice,
		Mode:                tts.VoiceMode(tmpl.Mode),
		Language:            tmpl.Language,
		Instruction:         tmpl.Instruction,
		ReferenceAudio:      tmpl.ReferenceAudio,
		ReferenceTranscript: tmpl.ReferenceTranscript,
	})
	if err != nil {
		return nil, 0, err
	}
	return drainPCMStream(ctx, result.Stream, result.SampleRate)
}

// finishScene persists the manifest (stamping CreatedAt outside the
// deterministic core), encodes only the mix on a clean run, and prints one
// summary. renderErr is returned after reporting, so a script reading --json
// gets the counts alongside a non-zero exit.
func finishScene(cmd *cobra.Command, opts scene.Options, result scene.Result, enc encoder.Encoder, renderErr error) error {
	manifestPath := scene.ManifestPath(opts.OutDir)
	m := result.Manifest
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := scene.WriteRunManifest(manifestPath, m); err != nil {
		return err
	}

	// WAV is the source of truth and the per-turn files are never encoded: the
	// mix is the deliverable, and an encode failure leaves everything on disk.
	encoded := []string{}
	if renderErr == nil && enc != nil && result.Conversation != "" {
		out, err := render.EncodeWAVs(cmd.Context(), enc, []string{result.Conversation})
		if err != nil {
			return fmt.Errorf("scene: %w", err)
		}
		encoded = out
	}

	complete, skipped, failed := m.Counts()
	out := cmd.OutOrStdout()
	if opts.JSON {
		if err := writeRenderJSON(out, map[string]any{
			"output_dir":      opts.OutDir,
			"conversation":    result.Conversation,
			"manifest":        manifestPath,
			"script_resolved": filepath.Join(opts.OutDir, scene.ResolvedScriptFile),
			"turns":           len(m.Turns),
			"completed":       complete,
			"skipped":         skipped,
			"failed":          failed,
			"encoded":         encoded,
			"sample_rate":     m.SampleRate,
			"duration_ms":     m.DurationMS,
		}); err != nil {
			return err
		}
		return renderErr
	}
	writeSceneSummary(out, m, manifestPath, result.Conversation, encoded)
	return renderErr
}

// writeSceneSummary prints the human tail of a run.
func writeSceneSummary(out io.Writer, m scene.RunManifest, manifestPath, conversation string, encoded []string) {
	complete, skipped, failed := m.Counts()
	fmt.Fprintf(out, "  Recorded %d turn(s) (%d skipped, %d failed)\n", complete, skipped, failed)
	if conversation != "" {
		fmt.Fprintf(out, "  Conversation: %s (%s)\n", conversation, time.Duration(m.DurationMS)*time.Millisecond)
	}
	fmt.Fprintf(out, "  Manifest:     %s\n", manifestPath)
	for _, e := range encoded {
		fmt.Fprintf(out, "  Encoded:      %s\n", e)
	}
}
