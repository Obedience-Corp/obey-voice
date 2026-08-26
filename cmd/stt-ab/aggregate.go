package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Schema is the artifact contract. L2 predates the rename and still says
// samantha.bench.stt-ab.v1; new artifacts use this one.
const Schema = "obey-voice.bench.stt-ab.v1"

// percentileMethod is recorded in every artifact so two runs are comparable
// without anyone having to guess how p90 was derived.
const percentileMethod = "linear-interpolation-on-sorted-finals"

// maxSampleTranscripts matches the count L4 carries.
const maxSampleTranscripts = 4

// rawResult mirrors the benchmarkResult written by `obey-voice benchmark`
// (cmd/obey-voice/cmd/benchmark.go). It is unexported there, so this is a
// deliberate copy of the wire shape rather than a shared type; only the fields
// the aggregation needs are declared.
type rawResult struct {
	Mode            string     `json:"mode"`
	Provider        string     `json:"provider"`
	Iteration       int        `json:"iteration"`
	Fixture         string     `json:"fixture"`
	Transcript      string     `json:"transcript"`
	Expected        string     `json:"expected"`
	TranscriptScore float64    `json:"transcript_score"`
	Metrics         rawMetrics `json:"metrics"`
	Errors          []rawError `json:"errors"`
}

// rawMetrics reads events.TurnMetrics, which carries no json tags — its keys
// are Go field names, and its durations marshal as int64 nanoseconds.
type rawMetrics struct {
	STTFinalElapsed int64 `json:"STTFinalElapsed"`
}

type rawError struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// hasFinal reports whether the run produced a FinalTranscript.
//
// STTFinalElapsed is assigned only on stt.KindFinalTranscript
// (benchmark.go), so zero means no final arrived — the run may still carry
// text from a trailing partial. That is exactly L2's failure: a transcript
// that looks plausible with no final behind it. Counting empty transcripts
// instead would have scored those 6 runs as successes.
func (r rawResult) hasFinal() bool { return r.Metrics.STTFinalElapsed > 0 }

func (r rawResult) sttFinalMS() float64 {
	return float64(r.Metrics.STTFinalElapsed) / 1e6
}

// ProviderStats is one provider's column in the artifact.
type ProviderStats struct {
	Runs                        int      `json:"runs"`
	Fixtures                    int      `json:"fixtures"`
	Iterations                  int      `json:"iterations"`
	RunsWithoutAFinalTranscript int      `json:"runs_without_a_final_transcript"`
	RunsWithErrors              int      `json:"runs_with_errors,omitempty"`
	MeanTranscriptScore         *float64 `json:"mean_transcript_score"`
	MedianSTTFinalMS            *float64 `json:"median_stt_final_ms"`
	P90STTFinalMS               *float64 `json:"p90_stt_final_ms"`
	SampleTranscripts           []string `json:"sample_transcripts"`
}

// Result mirrors L4's decision block. The three *_criterion_met verdicts stay
// null: "met" is a judgement against criteria fixed before the numbers
// existed, which arithmetic cannot assert. FinalsRegressed is computed because
// losing finals is the one failure L2 proved can hide behind good averages.
type Result struct {
	AccuracyGapPoints       *float64 `json:"accuracy_gap_points"`
	AccuracyCriterionMet    *bool    `json:"accuracy_criterion_met"`
	LatencyDeltaMS          *float64 `json:"latency_delta_ms"`
	LatencyCriterionMet     *bool    `json:"latency_criterion_met"`
	ReliabilityCriterionMet *bool    `json:"reliability_criterion_met"`
	FinalsRegressed         bool     `json:"finals_regressed"`
	Decision                string   `json:"decision"`
}

// Report is the full artifact, keyed to match L4 so the output can be
// committed to docs/benchmarks/ directly.
type Report struct {
	Schema                 string                   `json:"schema"`
	Item                   string                   `json:"item"`
	Date                   string                   `json:"date"`
	Question               string                   `json:"question"`
	CriteriaFixedInAdvance map[string]string        `json:"criteria_fixed_in_advance"`
	Control                string                   `json:"control"`
	Candidate              string                   `json:"candidate"`
	Providers              map[string]ProviderStats `json:"providers"`
	Result                 Result                   `json:"result"`
	PercentileMethod       string                   `json:"percentile_method"`
	WarmupDropped          bool                     `json:"warmup_dropped"`
	ReadingTheNumbers      string                   `json:"reading_the_numbers"`
	EvidenceLimits         string                   `json:"evidence_limits"`
	DurableValue           string                   `json:"durable_value"`
	Provenance             Provenance               `json:"provenance"`
}

// Provenance carries the keys L4 uses. Backend is new: a GPU run that silently
// fell back to CPU is not a GPU result, and no existing key recorded that.
type Provenance struct {
	ObeyVoiceCommit string `json:"obey_voice_commit"`
	Machine         string `json:"machine"`
	CPU             string `json:"cpu"`
	OS              string `json:"os"`
	Backend         string `json:"backend,omitempty"`
	Fixtures        string `json:"fixtures"`
	Iterations      int    `json:"iterations"`
	Command         string `json:"command"`
	Aggregator      string `json:"aggregator"`
}

// benchmarkEnvelope is the shape scripts/bench.sh writes: the raw array
// wrapped with provenance and thresholds. Committed artifacts (baseline.json,
// L1, L3, N3, N6) are envelopes, while a fresh --json-file run is a bare
// array, so both have to be readable.
type benchmarkEnvelope struct {
	Results []rawResult `json:"results"`
}

// ParseResults decodes either a bare `obey-voice benchmark --json-file` array
// or a bench.sh envelope.
//
// Note `json.MarshalIndent` writes a nil slice as literal null, so an empty
// run reaches us as "null" rather than "[]"; both land as zero results.
func ParseResults(data []byte) ([]rawResult, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("benchmark json was empty")
	}

	var results []rawResult
	if trimmed[0] == '{' {
		var envelope benchmarkEnvelope
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, fmt.Errorf("decode benchmark envelope: %w", err)
		}
		results = envelope.Results
	} else if err := json.Unmarshal(trimmed, &results); err != nil {
		return nil, fmt.Errorf("decode benchmark json: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("benchmark json contained no results")
	}
	return results, nil
}

// DropWarmup removes iteration 1, which scripts/bench.sh excludes from its
// latency medians as a warm-up run.
//
// The committed stt-ab records did NOT do this — L4 reports runs: 42 for 14
// fixtures x 3 iterations — so it is off by default and recorded in the
// artifact either way. Silently differing here makes two artifacts
// incomparable while looking identical.
func DropWarmup(results []rawResult) []rawResult {
	kept := make([]rawResult, 0, len(results))
	for _, r := range results {
		if r.Iteration > 1 {
			kept = append(kept, r)
		}
	}
	return kept
}

// STTRunsByProvider keeps mode=="stt" runs, grouped by the provider alias the
// benchmark recorded.
func STTRunsByProvider(results []rawResult) map[string][]rawResult {
	byProvider := make(map[string][]rawResult)
	for _, r := range results {
		if r.Mode != "stt" || r.Provider == "" {
			continue
		}
		byProvider[r.Provider] = append(byProvider[r.Provider], r)
	}
	return byProvider
}

// StatsFor aggregates one provider's runs.
//
// Latency percentiles cover only runs that produced a final. Including the
// zeros would drag the median toward nothing and make a provider that mostly
// failed look fast; the failures are reported as their own count instead.
func StatsFor(runs []rawResult) ProviderStats {
	stats := ProviderStats{Runs: len(runs)}

	fixtures := make(map[string]struct{})
	var finals []float64
	var scores []float64
	var samples []string
	seenSample := make(map[string]struct{})

	for _, r := range runs {
		if r.Fixture != "" {
			fixtures[r.Fixture] = struct{}{}
		}
		if r.Iteration > stats.Iterations {
			stats.Iterations = r.Iteration
		}
		if len(r.Errors) > 0 {
			stats.RunsWithErrors++
		}
		if r.hasFinal() {
			finals = append(finals, r.sttFinalMS())
		} else {
			stats.RunsWithoutAFinalTranscript++
		}
		// A run with no expected text has nothing to score against; a run
		// that scored exactly 0 is absent from the JSON (omitempty) and
		// correctly reads back as 0, which is a real data point.
		if r.Expected != "" {
			scores = append(scores, r.TranscriptScore)
		}
		if r.Transcript != "" && len(samples) < maxSampleTranscripts {
			if _, dup := seenSample[r.Transcript]; !dup {
				seenSample[r.Transcript] = struct{}{}
				samples = append(samples, r.Transcript)
			}
		}
	}

	stats.Fixtures = len(fixtures)
	stats.SampleTranscripts = samples
	stats.MeanTranscriptScore = roundPtr(mean(scores), 4)
	stats.MedianSTTFinalMS = roundPtr(percentile(finals, 50), 1)
	stats.P90STTFinalMS = roundPtr(percentile(finals, 90), 1)
	return stats
}

// Compare fills the decision block's arithmetic. Prose and criterion verdicts
// stay for a human.
func Compare(control, candidate ProviderStats) Result {
	var res Result
	if control.MeanTranscriptScore != nil && candidate.MeanTranscriptScore != nil {
		gap := (*candidate.MeanTranscriptScore - *control.MeanTranscriptScore) * 100
		res.AccuracyGapPoints = roundPtr(&gap, 1)
	}
	if control.MedianSTTFinalMS != nil && candidate.MedianSTTFinalMS != nil {
		delta := *candidate.MedianSTTFinalMS - *control.MedianSTTFinalMS
		res.LatencyDeltaMS = roundPtr(&delta, 1)
	}
	res.FinalsRegressed = candidate.RunsWithoutAFinalTranscript > control.RunsWithoutAFinalTranscript
	return res
}

func mean(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	out := sum / float64(len(values))
	return &out
}

// percentile interpolates linearly between order statistics. Returns nil for
// an empty set so the artifact carries null rather than a fabricated zero.
func percentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		out := sorted[0]
		return &out
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		out := sorted[lower]
		return &out
	}
	frac := rank - float64(lower)
	out := sorted[lower] + frac*(sorted[upper]-sorted[lower])
	return &out
}

func roundPtr(v *float64, places int) *float64 {
	if v == nil {
		return nil
	}
	shift := math.Pow(10, float64(places))
	out := math.Round(*v*shift) / shift
	return &out
}
