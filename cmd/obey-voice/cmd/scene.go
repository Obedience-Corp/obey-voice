package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/scene"
)

// sceneRunner executes a validated scene invocation. The cgo command layer
// supplies the synthesizing runner; the integration build supplies the
// plan-only runner. This keeps the command definition (flags + validation)
// cgo-free and shared across both binaries.
type sceneRunner func(cmd *cobra.Command, opts scene.Options) error

// errSceneGenerateUnsupported is returned for a generated scene until the
// resolver that writes the dialogue lands. It is raised after the script has
// been validated, so a broken script still reports its own problem first.
var errSceneGenerateUnsupported = errors.New("scene: generated mode is not in this build")

// newSceneCmd builds the `obey-voice scene` command. It parses and validates
// flags (cgo-free) and delegates execution to run.
func newSceneCmd(run sceneRunner) *cobra.Command {
	var opts scene.Options

	cmd := &cobra.Command{
		Use:   "scene SCRIPT",
		Short: "Record a scripted two-persona conversation to audio (batch, scriptable)",
		Long: `Record a written conversation between two personas as audio files.

A scene script is an obey-voice.scene.v1 YAML document: a cast of two personas
and the turns they speak. Each persona brings its own voice, so the recording
is a conversation rather than one narrator reading both parts. This is batch
work — no microphone, no live pipeline, no interaction.

Examples:
  obey-voice scene testdata/scenes/two-voices.verbatim.yaml --out-dir out/coffee
  obey-voice scene script.yaml --out-dir out/scene --pause 250ms

Overrides:
  --mode, --pause and --max-turns default to empty: the script document wins
  unless you actually pass the flag. Unset, a scene is verbatim with 400ms
  between turns.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Script = args[0]
			f := cmd.Flags()
			opts.ModeSet = f.Changed("mode")
			opts.PauseSet = f.Changed("pause")
			opts.MaxTurnsSet = f.Changed("max-turns")
			if err := opts.Validate(); err != nil {
				return err
			}
			return run(cmd, opts)
		},
	}

	addSceneFlags(cmd, &opts)
	return cmd
}

// addSceneFlags registers the scene flags. The three override flags default to
// empty on purpose: RunE reads Flags().Changed to tell "the operator asked for
// this" from "the flag was never touched", so the script document keeps its
// authored values.
func addSceneFlags(cmd *cobra.Command, opts *scene.Options) {
	f := cmd.Flags()
	f.StringVar(&opts.OutDir, "out-dir", "", "Write turn WAVs, conversation.wav and a manifest to DIR (required)")
	f.StringVar(&opts.Mode, "mode", "", "Override the script's mode: verbatim|generate (default: the script's, else verbatim)")
	f.StringVar(&opts.Pause, "pause", "", "Override the silence between turns (Go duration, e.g. 250ms; default 400ms)")
	f.IntVar(&opts.MaxTurns, "max-turns", 0, "Override the generated conversation length (1-48; default 24)")
	f.BoolVar(&opts.JSON, "json", false, "Print a machine-readable summary")
	f.BoolVar(&opts.Resume, "resume", false, "Keep turns whose audio is already rendered and unchanged")
	f.BoolVar(&opts.Overwrite, "overwrite", false, "Replace existing outputs")
	f.StringVar(&opts.AudioFormat, "audio-format", "", "Also encode conversation.wav to mp3|m4a|m4b|aac|opus (WAV is always written)")
	f.StringVar(&opts.EncoderBin, "encoder", "", "External encoder binary to use (default: ffmpeg)")
	f.BoolVar(&opts.ResolveOnly, "resolve-only", false, "Write the resolved script without synthesizing audio")
	f.BoolVar(&opts.RequireDistinctVoices, "require-distinct-voices", false, "Fail instead of warning when the cast shares one voice")
}

// runScenePlan reports the resolved scene plan without recording anything. It
// is the runner the cgo-free integration binary ships: that build has no audio
// stack, so it validates the script and shows what the real binary would record.
func runScenePlan(cmd *cobra.Command, opts scene.Options) error {
	return runScenePlanWith(cmd, opts, loadScenePersona)
}

// runScenePlanWith is runScenePlan with the persona store injected, so the plan
// path is testable without personas installed.
func runScenePlanWith(cmd *cobra.Command, opts scene.Options, loadPersona scene.PersonaLoader) error {
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

	errOut := cmd.ErrOrStderr()
	for _, warning := range plan.Warnings {
		fmt.Fprintf(errOut, "  warning: %s\n", warning)
	}
	writeScenePlan(cmd.OutOrStdout(), plan)
	return nil
}

// writeScenePlan prints the human plan: what would be recorded, by whom, and
// with which mouth.
func writeScenePlan(out io.Writer, plan *scene.Plan) {
	fmt.Fprintln(out, "  Scene plan")
	fmt.Fprintf(out, "    script: %s\n", plan.Script)
	fmt.Fprintf(out, "    output: %s\n", plan.OutDir)
	if plan.Title != "" {
		fmt.Fprintf(out, "    title:  %s\n", plan.Title)
	}
	fmt.Fprintf(out, "    mode:   %s\n", plan.Mode)
	fmt.Fprintf(out, "    pause:  %s\n", plan.Pause)
	fmt.Fprintf(out, "    turns:  %d\n", plan.Turns)
	fmt.Fprintln(out, "    cast:")
	for _, member := range plan.Cast {
		fmt.Fprintf(out, "      %s — %s (%s)\n", member.ID, member.DisplayName, member.Mouth)
	}
	fmt.Fprintln(out, "  (this build validates scripts only; no audio produced)")
}

// loadScenePersona resolves a cast persona, naming the ids that do exist when
// it does not — the same help EnsureAndApply gives for a bad active_persona.
func loadScenePersona(id string) (*persona.Profile, error) {
	profile, err := persona.Load(id)
	if err == nil {
		return profile, nil
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	available, _ := persona.List()
	ids := make([]string, 0, len(available))
	for _, candidate := range available {
		ids = append(ids, candidate.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("not found (no personas under %s)", persona.Dir())
	}
	return nil, fmt.Errorf("not found (available: %s)", strings.Join(ids, ", "))
}
