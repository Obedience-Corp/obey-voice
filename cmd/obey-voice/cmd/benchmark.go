//go:build !integration

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var (
	benchmarkPrompts             []string
	benchmarkJSON                benchmarkJSONFlag
	benchmarkJSONFile            string
	benchmarkIterations          int
	benchmarkAudioFixtures       []string
	benchmarkFullTurnFixtures    []string
	benchmarkExpectedTranscripts []string
	benchmarkSTTProviders        []string
	benchmarkTTSProviders        []string
	benchmarkTTSPlace            string
	benchmarkFixtureRealtime     bool
	benchmarkMaxTotal            time.Duration
	benchmarkMaxFirstModelChunk  time.Duration
	benchmarkMaxPlaybackStart    time.Duration
	benchmarkMaxPlaybackComplete time.Duration
	benchmarkMaxSTTFinal         time.Duration
	benchmarkMinTranscriptScore  float64
)

// ttsSpec is one leg of a TTS sweep: a provider and, for a multi-tier
// provider, the model size. Written "qwen3-tts:1.7b" on the command line so
// the sweep stays one repeatable flag, exactly like --stt-provider.
type ttsSpec struct {
	provider string
	tier     string
}

// String is the round-trip of the flag value, and "" for the configured
// provider (no override).
func (t ttsSpec) String() string {
	if t.provider == "" {
		return ""
	}
	if t.tier == "" {
		return t.provider
	}
	return t.provider + ":" + t.tier
}

// apply overrides the TTS selection on a config copy. An empty spec leaves the
// configured provider alone.
func (t ttsSpec) apply(cfg *config.Config) {
	if t.provider == "" {
		return
	}
	cfg.TTSProvider = t.provider
	if t.tier != "" {
		cfg.QwenTTSModelTier = t.tier
	}
}

// parseTTSSpecs turns the --tts-provider values into sweep legs. No values
// means one leg using the configured provider, so every mode keeps its
// existing single-run behaviour.
func parseTTSSpecs(values []string) ([]ttsSpec, error) {
	if len(values) == 0 {
		return []ttsSpec{{}}, nil
	}
	specs := make([]ttsSpec, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("--tts-provider: empty value")
		}
		provider, tier, hasTier := strings.Cut(v, ":")
		provider, tier = strings.TrimSpace(provider), strings.TrimSpace(tier)
		if provider == "" {
			return nil, fmt.Errorf("--tts-provider %q: missing provider before the tier", v)
		}
		if hasTier && tier == "" {
			return nil, fmt.Errorf("--tts-provider %q: empty tier after ':'", v)
		}
		specs = append(specs, ttsSpec{provider: provider, tier: tier})
	}
	return specs, nil
}

// applyBenchmarkTTSPlace overrides where speech is synthesized for this run.
//
// Place is app-level topology rather than one leg of a sweep: a single
// benchmark process holds one speech transport, so "the same fixtures against
// both computers" is two runs, one per place, compared through their
// artifacts. Having the flag at all is what makes those two runs the same
// instrument — otherwise the operator edits config between them and the
// difference is no longer only the place.
//
// An invalid topology fails here rather than at the first synthesis, where it
// would fall back to Kokoro and quietly produce a Kokoro measurement labelled
// lan.
func applyBenchmarkTTSPlace(cfg *config.Config, place string) error {
	place = strings.ToLower(strings.TrimSpace(place))
	if place == "" {
		return nil
	}
	cfg.TTSPlace = place
	if err := config.ValidateTTSPlace(cfg); err != nil {
		return fmt.Errorf("--tts-place %s: %w", place, err)
	}
	return nil
}

// benchmarkJSONFlag backs --json (G38): every other --json in this CLI is a
// bool, but this one used to be a string file path. It implements
// pflag.Value directly (with NoOptDefVal "true") so bare --json still parses
// as a bool while a path value is accepted for one release, deprecated in
// favour of --json-file.
type benchmarkJSONFlag struct {
	stdout bool   // true: print the results array to stdout
	path   string // non-empty: deprecated string-path form
}

func (f *benchmarkJSONFlag) String() string {
	if f.path != "" {
		return f.path
	}
	if f.stdout {
		return "true"
	}
	return "false"
}

func (f *benchmarkJSONFlag) Set(v string) error {
	switch strings.ToLower(v) {
	case "true", "1", "":
		f.stdout, f.path = true, ""
	case "false", "0":
		f.stdout, f.path = false, ""
	default:
		// Anything else is the deprecated path form: cobra/pflag reject an
		// unparsable bool value outright for a real BoolVar, but this flag's
		// whole point is accepting that value for one more release.
		f.stdout, f.path = false, v
	}
	return nil
}

func (f *benchmarkJSONFlag) Type() string { return "string" }

// resolveBenchmarkOutput reports whether results should be encoded to
// stdout, which file (if any) they should be written to, and whether the
// deprecated string form of --json was used (so the caller can warn on
// stderr). An explicit --json-file always wins the file path when both are
// set, so there is never ambiguity about which one is authoritative.
//
// leftoverArgs handles the space-separated legacy invocation specifically:
// `--json <path>` was the *only* form the old StringVar ever accepted, but
// pflag's NoOptDefVal (needed so bare --json parses as a bool) has no way to
// bind a value from a separate token — `--json /tmp/out.json` parses as
// bare --json (true) with "/tmp/out.json" left over as a positional
// argument. benchmarkCmd defines no positional arguments of its own, so a
// single leftover argument immediately following a bare --json can only be
// this legacy form; treat it exactly like the deprecated --json=<path> form
// rather than silently dropping it and switching to stdout JSON.
func resolveBenchmarkOutput(jsonFlag benchmarkJSONFlag, jsonFile string, leftoverArgs []string) (stdout bool, file string, deprecated bool) {
	if jsonFlag.stdout && jsonFlag.path == "" && len(leftoverArgs) == 1 {
		jsonFlag = benchmarkJSONFlag{path: leftoverArgs[0]}
	}
	file = jsonFile
	if jsonFlag.path != "" {
		deprecated = true
		if file == "" {
			file = jsonFlag.path
		}
	}
	return jsonFlag.stdout, file, deprecated
}

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run a local Obey Voice benchmark",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		// The deprecated string form of --json still writes a file — same
		// as before — but a caller who also passed --json-file wins outright
		// (no ambiguity about which path is authoritative).
		stdout, jsonFile, deprecated := resolveBenchmarkOutput(benchmarkJSON, benchmarkJSONFile, args)
		if deprecated {
			fmt.Fprintln(os.Stderr, "--json <path> is deprecated; use --json-file <path>")
		}

		// A TTS sweep with the voice disabled would produce a full set of
		// results in which nothing was ever synthesized. Fail rather than
		// write an artifact that reads like a measurement.
		if len(benchmarkTTSProviders) > 0 && noVoice {
			return errors.New("--tts-provider cannot be combined with --no-voice: no audio would be synthesized")
		}

		if err := applyBenchmarkTTSPlace(cfg, benchmarkTTSPlace); err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		var results []benchmarkResult
		switch {
		case len(benchmarkFullTurnFixtures) > 0:
			results, err = runFullTurnBenchmarks(ctx, cfg)
		case len(benchmarkAudioFixtures) > 0:
			results, err = runSTTBenchmarks(ctx, cfg)
		default:
			results, err = runTextBenchmarks(ctx, cfg)
		}
		if err != nil {
			return err
		}

		if stdout {
			if err := encodeJSON(cmd, results); err != nil {
				return fmt.Errorf("encode benchmark results: %w", err)
			}
		} else {
			printBenchmarkSummary(results)
		}
		if jsonFile != "" {
			if err := writeBenchmarkJSON(jsonFile, results); err != nil {
				return err
			}
			fmt.Printf("\n  %s %s\n\n", okStyle.Render("Wrote benchmark JSON to"), jsonFile)
		}

		return benchmarkExitErr(results)
	},
}

func init() {
	benchmarkCmd.Flags().StringArrayVar(&benchmarkPrompts, "prompt", nil, "Benchmark prompt (repeatable)")
	benchmarkCmd.Flags().Var(&benchmarkJSON, "json", "Print benchmark results as a JSON array on stdout (a path value is deprecated — use --json-file)")
	benchmarkCmd.Flags().Lookup("json").NoOptDefVal = "true"
	benchmarkCmd.Flags().StringVar(&benchmarkJSONFile, "json-file", "", "Write benchmark results to a JSON file")
	benchmarkCmd.Flags().IntVar(&benchmarkIterations, "iterations", 1, "Number of times to run each benchmark prompt")
	benchmarkCmd.Flags().StringArrayVar(&benchmarkAudioFixtures, "audio-fixture", nil, "WAV fixture for STT benchmarking (repeatable)")
	benchmarkCmd.Flags().StringArrayVar(&benchmarkFullTurnFixtures, "full-turn-fixture", nil, "WAV fixture driven through the full voice turn: capture, VAD, STT, brain, TTS (repeatable)")
	benchmarkCmd.Flags().StringArrayVar(&benchmarkExpectedTranscripts, "expect-text", nil, "Expected transcript for each fixture (repeatable)")
	benchmarkCmd.Flags().StringArrayVar(&benchmarkSTTProviders, "stt-provider", nil, "STT provider(s) to benchmark in fixture mode (repeatable)")
	benchmarkCmd.Flags().StringArrayVar(&benchmarkTTSProviders, "tts-provider", nil, "TTS provider(s) to benchmark, optionally with a model tier: kokoro, qwen3-tts:0.6b, qwen3-tts:1.7b (repeatable)")
	benchmarkCmd.Flags().StringVar(&benchmarkTTSPlace, "tts-place", "", "Where speech is synthesized for this run: local or lan (default: the configured tts_place)")
	benchmarkCmd.Flags().BoolVar(&benchmarkFixtureRealtime, "fixture-realtime", true, "Replay fixture audio in real time for latency measurements")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxTotal, "max-total", 0, "Fail when total benchmark time exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxFirstModelChunk, "max-first-model-chunk", 0, "Fail when first model chunk exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxPlaybackStart, "max-playback-start", 0, "Fail when playback start exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxPlaybackComplete, "max-playback-complete", 0, "Fail when playback completion exceeds this duration")
	benchmarkCmd.Flags().DurationVar(&benchmarkMaxSTTFinal, "max-stt-final", 0, "Fail when STT finalization exceeds this duration")
	benchmarkCmd.Flags().Float64Var(&benchmarkMinTranscriptScore, "min-transcript-score", 0, "Fail when transcript score falls below this value (0-1)")
	rootCmd.AddCommand(benchmarkCmd)
}
