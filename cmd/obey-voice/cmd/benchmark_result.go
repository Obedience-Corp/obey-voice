//go:build !integration

package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type benchmarkResult struct {
	Mode     string `json:"mode"`
	Provider string `json:"provider,omitempty"`
	// TTSRequested is the --tts-provider value this run was asked to use.
	// What actually synthesized is in Metrics.TTSProvider / Metrics.TTSTier:
	// a run that silently fell back to Kokoro is not a measurement of the
	// provider that was requested, and only the pair shows that.
	TTSRequested    string              `json:"tts_requested,omitempty"`
	Iteration       int                 `json:"iteration"`
	Prompt          string              `json:"prompt,omitempty"`
	Fixture         string              `json:"fixture,omitempty"`
	Elapsed         time.Duration       `json:"elapsed"`
	Metrics         events.TurnMetrics  `json:"metrics"`
	Transcript      string              `json:"transcript,omitempty"`
	Expected        string              `json:"expected,omitempty"`
	TranscriptScore float64             `json:"transcript_score,omitempty"`
	Errors          []benchmarkErrorLog `json:"errors,omitempty"`
	// Recovered holds the events that report a rescue rather than a failure -
	// today only events.StageTTSFallback. They are kept out of Errors because
	// countErrorResults reads that field to decide the exit status, and a turn
	// the fallback voice completed is degraded, not failed.
	Recovered  []benchmarkErrorLog `json:"recovered,omitempty"`
	Violations []string            `json:"violations,omitempty"`
}

type benchmarkErrorLog struct {
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message"`
}

func drainBenchmarkChannels(metricsCh chan events.TurnMetrics, errorCh chan events.Error) {
	for {
		select {
		case <-metricsCh:
		case <-errorCh:
		default:
			return
		}
	}
}

func readBenchmarkMetrics(metricsCh chan events.TurnMetrics) events.TurnMetrics {
	select {
	case metric := <-metricsCh:
		return metric
	default:
		return events.TurnMetrics{}
	}
}

// readBenchmarkErrors drains the bus, separating recoveries from failures.
//
// events.StageTTSFallback announces that Kokoro spoke a sentence the primary
// engine could not, so the turn produced audio: counting it as an error made
// `benchmark` exit non-zero on a successful rescue and read a recovered run as
// a failed one. It is still reported — a reply that changed voice mid-way is
// not a clean measurement of the engine that was asked for — but it is
// reported as what it is.
func readBenchmarkErrors(errorCh chan events.Error) (errs, recovered []benchmarkErrorLog) {
	for {
		select {
		case err := <-errorCh:
			entry := benchmarkErrorLog{Stage: err.Stage, Message: err.Message}
			if err.Stage == events.StageTTSFallback {
				recovered = append(recovered, entry)
				continue
			}
			errs = append(errs, entry)
		default:
			return errs, recovered
		}
	}
}

func evaluateTextThresholds(result benchmarkResult) []string {
	var violations []string

	if benchmarkMaxTotal > 0 && result.Elapsed > benchmarkMaxTotal {
		violations = append(violations, fmt.Sprintf("total %s exceeded %s", result.Elapsed.Round(time.Millisecond), benchmarkMaxTotal))
	}
	if benchmarkMaxFirstModelChunk > 0 && (result.Metrics.FirstModelChunkElapsed <= 0 || result.Metrics.FirstModelChunkElapsed > benchmarkMaxFirstModelChunk) {
		violations = append(violations, fmt.Sprintf("first model chunk %s exceeded %s", formatMetric(result.Metrics.FirstModelChunkElapsed), benchmarkMaxFirstModelChunk))
	}
	if benchmarkMaxPlaybackStart > 0 && (result.Metrics.PlaybackStartElapsed <= 0 || result.Metrics.PlaybackStartElapsed > benchmarkMaxPlaybackStart) {
		violations = append(violations, fmt.Sprintf("playback start %s exceeded %s", formatMetric(result.Metrics.PlaybackStartElapsed), benchmarkMaxPlaybackStart))
	}
	if benchmarkMaxPlaybackComplete > 0 && (result.Metrics.PlaybackCompleteElapsed <= 0 || result.Metrics.PlaybackCompleteElapsed > benchmarkMaxPlaybackComplete) {
		violations = append(violations, fmt.Sprintf("playback complete %s exceeded %s", formatMetric(result.Metrics.PlaybackCompleteElapsed), benchmarkMaxPlaybackComplete))
	}
	return violations
}

func evaluateSTTThresholds(result benchmarkResult) []string {
	var violations []string

	if benchmarkMaxSTTFinal > 0 && (result.Metrics.STTFinalElapsed <= 0 || result.Metrics.STTFinalElapsed > benchmarkMaxSTTFinal) {
		violations = append(violations, fmt.Sprintf("stt final %s exceeded %s", formatMetric(result.Metrics.STTFinalElapsed), benchmarkMaxSTTFinal))
	}
	if benchmarkMinTranscriptScore > 0 && result.TranscriptScore < benchmarkMinTranscriptScore {
		violations = append(violations, fmt.Sprintf("transcript score %.2f below %.2f", result.TranscriptScore, benchmarkMinTranscriptScore))
	}
	return violations
}

func countViolations(results []benchmarkResult) int {
	total := 0
	for _, result := range results {
		total += len(result.Violations)
	}
	return total
}

func countErrorResults(results []benchmarkResult) int {
	total := 0
	for _, result := range results {
		if len(result.Errors) > 0 {
			total++
		}
	}
	return total
}

// benchmarkExitErr decides the command's exit status from the collected
// results: hard errors and threshold violations both fail the run.
func benchmarkExitErr(results []benchmarkResult) error {
	errored := countErrorResults(results)
	violations := countViolations(results)
	switch {
	case errored > 0 && violations > 0:
		return fmt.Errorf("benchmark failed: %d run(s) with errors, %d threshold violation(s)", errored, violations)
	case errored > 0:
		return fmt.Errorf("benchmark failed: %d run(s) with errors", errored)
	case violations > 0:
		return fmt.Errorf("benchmark failed: %d threshold violation(s)", violations)
	default:
		return nil
	}
}

func transcriptScore(expected, actual string) float64 {
	exp := tokenizeTranscript(expected)
	act := tokenizeTranscript(actual)
	if len(exp) == 0 {
		if len(act) == 0 {
			return 1
		}
		return 0
	}

	expCounts := make(map[string]int, len(exp))
	for _, token := range exp {
		expCounts[token]++
	}

	matches := 0
	for _, token := range act {
		if expCounts[token] > 0 {
			matches++
			expCounts[token]--
		}
	}

	precision := float64(matches) / float64(max(len(act), 1))
	recall := float64(matches) / float64(len(exp))
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func tokenizeTranscript(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}

	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, text)
	return strings.Fields(clean)
}
