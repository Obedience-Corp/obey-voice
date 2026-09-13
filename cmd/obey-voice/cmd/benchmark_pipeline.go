//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

var defaultBenchmarkPrompts = []string{
	"Explain in one sentence why fast feedback matters in software development.",
	"Give me two short sentences on how to review a pull request carefully.",
	"Summarize why barge-in matters for a voice assistant in two sentences.",
}

func runTextBenchmarks(ctx context.Context, cfg *config.Config) ([]benchmarkResult, error) {
	specs, err := parseTTSSpecs(benchmarkTTSProviders)
	if err != nil {
		return nil, err
	}
	var results []benchmarkResult
	for _, spec := range specs {
		// One pipeline per leg: the TTS provider is bound at build time, so a
		// sweep cannot reuse a pipeline across legs.
		legResults, err := runTextBenchmarkLeg(ctx, cfg, spec)
		results = append(results, legResults...)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func runTextBenchmarkLeg(ctx context.Context, cfg *config.Config, spec ttsSpec) ([]benchmarkResult, error) {
	cfgCopy := *cfg
	spec.apply(&cfgCopy)
	cfg = &cfgCopy

	bus := events.NewBus()
	p, cleanup, err := buildPipeline(ctx, cfg, bus, true, noVoice, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("init benchmark pipeline: %w", err)
	}
	defer cleanup()

	metricsCh := make(chan events.TurnMetrics, 64)
	errorCh := make(chan events.Error, 64)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		select {
		case metricsCh <- e:
		default:
		}
	})
	events.Subscribe(bus, func(e events.Error) {
		select {
		case errorCh <- e:
		default:
		}
	})

	prompts := benchmarkPrompts
	if len(prompts) == 0 {
		prompts = defaultBenchmarkPrompts
	}
	if benchmarkIterations < 1 {
		benchmarkIterations = 1
	}

	var results []benchmarkResult
	for iteration := 1; iteration <= benchmarkIterations; iteration++ {
		for _, prompt := range prompts {
			drainBenchmarkChannels(metricsCh, errorCh)
			p.Brain.ClearHistory()

			start := time.Now()
			runErr := p.RunTurnTextMode(ctx, prompt)
			elapsed := time.Since(start)
			metric := readBenchmarkMetrics(metricsCh)
			errs, recovered := readBenchmarkErrors(errorCh)

			if runErr != nil {
				errs = append(errs, benchmarkErrorLog{Stage: "benchmark", Message: runErr.Error()})
			}

			result := benchmarkResult{
				Mode:         "text",
				TTSRequested: spec.String(),
				Iteration:    iteration,
				Prompt:       prompt,
				Elapsed:      elapsed,
				Metrics:      metric,
				Errors:       errs,
				Recovered:    recovered,
			}
			result.Violations = evaluateTextThresholds(result)
			results = append(results, result)
		}
	}

	return results, nil
}

// runFullTurnBenchmarks drives recorded utterances through the whole voice
// turn — capture, VAD, STT, brain, TTS, playback — via RunTurn.
//
// This is the only mode that measures what the voice pipeline actually does.
// Fixture mode stops at the final transcript and never reaches the brain; text
// mode skips capture and STT entirely and synthesizes the whole reply as one
// segment, so neither can show a first-sentence or endpointing change. Only
// RunTurn populates FirstSegmentElapsed and FirstAudioReadyElapsed from real
// sentence chunking.
func runFullTurnBenchmarks(ctx context.Context, cfg *config.Config) ([]benchmarkResult, error) {
	if len(benchmarkExpectedTranscripts) > 0 && len(benchmarkExpectedTranscripts) != len(benchmarkFullTurnFixtures) {
		return nil, fmt.Errorf("--expect-text count (%d) must match --full-turn-fixture count (%d)", len(benchmarkExpectedTranscripts), len(benchmarkFullTurnFixtures))
	}
	if benchmarkIterations < 1 {
		benchmarkIterations = 1
	}

	providers := benchmarkSTTProviders
	if len(providers) == 0 {
		providers = []string{""} // configured provider
	}
	ttsSpecs, err := parseTTSSpecs(benchmarkTTSProviders)
	if err != nil {
		return nil, err
	}

	var results []benchmarkResult
	for _, spec := range ttsSpecs {
		for _, providerName := range providers {
			for iteration := 1; iteration <= benchmarkIterations; iteration++ {
				for idx, fixture := range benchmarkFullTurnFixtures {
					expected := ""
					if idx < len(benchmarkExpectedTranscripts) {
						expected = benchmarkExpectedTranscripts[idx]
					}
					result, err := runSingleFullTurnBenchmark(ctx, cfg, providerName, spec, fixture, expected, iteration)
					if err != nil {
						result.Errors = append(result.Errors, benchmarkErrorLog{Stage: "benchmark", Message: err.Error()})
					}
					result.Violations = fullTurnViolations(result)
					results = append(results, result)

					if ctx.Err() != nil {
						// Surface the cancellation: a truncated run must not be
						// written as if it were a complete one.
						return results, ctx.Err()
					}
				}
			}
		}
	}
	return results, nil
}

// fullTurnViolations evaluates a full-turn result against both threshold
// families: a turn crosses the latency budgets and the transcript budget, and
// checking only one would let the other regress unnoticed.
func fullTurnViolations(result benchmarkResult) []string {
	return append(evaluateTextThresholds(result), evaluateSTTThresholds(result)...)
}

// runSingleFullTurnBenchmark builds a pipeline whose microphone is a WAV and
// runs exactly one turn through it.
//
// The pipeline is rebuilt per turn on purpose: audio.FixtureSource is
// single-shot (Exhausted), and the STT provider retains it, so a reused
// pipeline would replay an empty source on the second turn. Rebuilding costs
// model load time, which is why iteration 1 is treated as warmup by callers.
func runSingleFullTurnBenchmark(ctx context.Context, cfg *config.Config, providerName string, spec ttsSpec, fixture, expected string, iteration int) (benchmarkResult, error) {
	result := benchmarkResult{
		Mode:         "voice",
		Provider:     providerName,
		TTSRequested: spec.String(),
		Iteration:    iteration,
		Fixture:      fixture,
		Expected:     expected,
	}

	cfgCopy := *cfg
	spec.apply(&cfgCopy)
	if providerName != "" {
		// An explicit alias must fully select the path rather than conflicting
		// with the user's configured mode.
		cfgCopy.STTProvider = providerName
		cfgCopy.STTMode = ""
	}
	// Every shipped STT backend rejects a nil VAD.
	cfgCopy.VADEnabled = true

	if err := config.EnsureRuntimeAssets(ctx, &cfgCopy, config.AssetRequest{NeedSTT: true, NeedVAD: true, NeedTTS: !noVoice}, nil); err != nil {
		return result, err
	}

	bus := events.NewBus()
	metricsCh := make(chan events.TurnMetrics, 64)
	errorCh := make(chan events.Error, 64)
	// Handlers run on the emitting goroutine, so they must never block.
	events.Subscribe(bus, func(e events.TurnMetrics) {
		select {
		case metricsCh <- e:
		default:
		}
	})
	events.Subscribe(bus, func(e events.Error) {
		select {
		case errorCh <- e:
		default:
		}
	})

	// text=true skips the live microphone; silent=noVoice keeps TTS and the
	// player unless the caller asked for silence.
	p, cleanup, err := buildPipeline(ctx, &cfgCopy, bus, true, noVoice, nil, nil)
	if err != nil {
		return result, fmt.Errorf("init full-turn pipeline: %w", err)
	}
	defer cleanup()

	source, err := audio.NewFixtureSourceFromWAV(fixture, audio.ChunkSize, benchmarkFixtureRealtime)
	if err != nil {
		return result, err
	}

	vad, err := audio.NewVAD(&cfgCopy)
	if err != nil {
		return result, err
	}
	defer vad.Delete()

	sttProvider, sttCleanup, err := stt.NewProvider(&cfgCopy, source, vad)
	if err != nil {
		return result, err
	}
	if sttCleanup != nil {
		defer sttCleanup()
	}

	p.STT = sttProvider
	p.VAD = vad
	// Capture stays nil deliberately. FixtureSource is not a pipeline.CaptureMonitor,
	// and transcribeTurn calls Capture.Reset() before every listen — a fixture
	// wired there would rewind the WAV and replay the same audio forever.
	// Barge-in is therefore not measured in this mode.

	drainBenchmarkChannels(metricsCh, errorCh)
	p.Brain.ClearHistory()

	start := time.Now()
	transcript, runErr := p.RunTurn(ctx)
	result.Elapsed = time.Since(start)
	result.Metrics = readBenchmarkMetrics(metricsCh)
	errs, recovered := readBenchmarkErrors(errorCh)
	result.Errors = append(result.Errors, errs...)
	result.Recovered = append(result.Recovered, recovered...)
	result.Transcript = transcript

	if expected != "" {
		result.TranscriptScore = transcriptScore(expected, transcript)
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}
