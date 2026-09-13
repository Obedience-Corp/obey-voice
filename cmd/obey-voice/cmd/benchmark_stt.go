//go:build !integration

package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

func runSTTBenchmarks(ctx context.Context, cfg *config.Config) ([]benchmarkResult, error) {
	if len(benchmarkExpectedTranscripts) > 0 && len(benchmarkExpectedTranscripts) != len(benchmarkAudioFixtures) {
		return nil, fmt.Errorf("--expect-text count (%d) must match --audio-fixture count (%d)", len(benchmarkExpectedTranscripts), len(benchmarkAudioFixtures))
	}

	providers := benchmarkSTTProviders
	if len(providers) == 0 {
		norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
		if err != nil {
			return nil, err
		}
		active := norm.Alias
		if active == "" {
			active = norm.Provider
		}
		providers = []string{active}
	}
	if benchmarkIterations < 1 {
		benchmarkIterations = 1
	}

	var results []benchmarkResult
	for _, providerName := range providers {
		for iteration := 1; iteration <= benchmarkIterations; iteration++ {
			for idx, fixture := range benchmarkAudioFixtures {
				expected := ""
				if idx < len(benchmarkExpectedTranscripts) {
					expected = benchmarkExpectedTranscripts[idx]
				}

				result, err := runSingleSTTBenchmark(ctx, cfg, providerName, fixture, expected, iteration)
				if err != nil {
					result.Errors = append(result.Errors, benchmarkErrorLog{Stage: "benchmark", Message: err.Error()})
				}
				result.Violations = append(result.Violations, evaluateSTTThresholds(result)...)
				results = append(results, result)
			}
		}
	}

	return results, nil
}

func runSingleSTTBenchmark(ctx context.Context, cfg *config.Config, providerName, fixture, expected string, iteration int) (benchmarkResult, error) {
	result := benchmarkResult{
		Mode:      "stt",
		Provider:  providerName,
		Iteration: iteration,
		Fixture:   fixture,
		Expected:  expected,
	}

	// providerName is an explicit alias; clear stt_mode so it fully selects
	// the path instead of conflicting with the user's configured mode.
	cfgCopy := *cfg
	cfgCopy.STTProvider = providerName
	cfgCopy.STTMode = ""

	if err := config.EnsureRuntimeAssets(ctx, &cfgCopy, config.AssetRequest{NeedSTT: true, NeedVAD: true}, nil); err != nil {
		return result, err
	}

	source, err := audio.NewFixtureSourceFromWAV(fixture, audio.ChunkSize, benchmarkFixtureRealtime)
	if err != nil {
		return result, err
	}

	vad, err := audio.NewVAD(&cfgCopy)
	if err != nil {
		return result, err
	}
	defer vad.Delete()

	provider, cleanup, err := stt.NewProvider(&cfgCopy, source, vad)
	if err != nil {
		return result, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	start := time.Now()
	session, err := provider.Start(ctx)
	if err != nil {
		return result, err
	}
	defer session.Close()

	var firstPartial time.Time
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				result.Elapsed = time.Since(start)
				if expected != "" {
					result.TranscriptScore = transcriptScore(expected, result.Transcript)
				}
				return result, nil
			}

			te := stt.ToTyped(event)
			switch te.Kind {
			case stt.KindPartialTranscript:
				if firstPartial.IsZero() {
					firstPartial = time.Now()
				}
				result.Transcript = te.Text
			case stt.KindFinalTranscript:
				now := time.Now()
				if firstPartial.IsZero() {
					firstPartial = now
				}
				result.Transcript = te.Text
				result.Elapsed = now.Sub(start)
				result.Metrics.STTFinalElapsed = result.Elapsed
				if !firstPartial.IsZero() {
					result.Metrics.FirstModelChunkElapsed = firstPartial.Sub(start)
				}
				if expected != "" {
					result.TranscriptScore = transcriptScore(expected, result.Transcript)
				}
				return result, nil
			case stt.KindTimeout:
				result.Elapsed = time.Since(start)
				return result, errors.New("stt timed out")
			case stt.KindFailure:
				result.Elapsed = time.Since(start)
				// The caller receives the error value itself; ErrText would
				// break a nil-Err Failure and drop the error chain.
				fail, _ := event.(stt.Failure)
				return result, fail.Err
			}
		}
	}
}
