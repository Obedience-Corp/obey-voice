package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Schema is the artifact contract, alongside obey-voice.bench.stt-ab.v1.
const Schema = "obey-voice.bench.tts-ab.v1"

// percentileMethod is recorded in every artifact so two runs are comparable
// without anyone having to guess how p90 was derived.
const percentileMethod = "linear-interpolation-on-sorted-turns"

// rawResult mirrors the benchmarkResult written by `obey-voice benchmark`
// (cmd/obey-voice/cmd/benchmark.go). It is unexported there, so this is a
// deliberate copy of the wire shape rather than a shared type; only the
// fields the aggregation needs are declared.
type rawResult struct {
	Mode         string     `json:"mode"`
	Provider     string     `json:"provider"`
	TTSRequested string     `json:"tts_requested"`
	Iteration    int        `json:"iteration"`
	Prompt       string     `json:"prompt"`
	Fixture      string     `json:"fixture"`
	Metrics      rawMetrics `json:"metrics"`
	Errors       []rawError `json:"errors"`
}

// rawMetrics reads events.TurnMetrics, which carries no json tags — its keys
// are Go field names, and its durations marshal as int64 nanoseconds.
type rawMetrics struct {
	Interrupted             bool         `json:"Interrupted"`
	FirstModelChunkElapsed  int64        `json:"FirstModelChunkElapsed"`
	ModelCompleteElapsed    int64        `json:"ModelCompleteElapsed"`
	FirstSegmentElapsed     int64        `json:"FirstSegmentElapsed"`
	FirstAudioReadyElapsed  int64        `json:"FirstAudioReadyElapsed"`
	PlaybackStartElapsed    int64        `json:"PlaybackStartElapsed"`
	PlaybackCompleteElapsed int64        `json:"PlaybackCompleteElapsed"`
	TTSProvider             string       `json:"TTSProvider"`
	TTSTier                 string       `json:"TTSTier"`
	TTSFallbacks            int          `json:"TTSFallbacks"`
	FirstAudioGrace         int64        `json:"FirstAudioGrace"`
	StallWatchdogFired      bool         `json:"StallWatchdogFired"`
	DeadAirElapsed          int64        `json:"DeadAirElapsed"`
	Segments                []rawSegment `json:"Segments"`
}

type rawSegment struct {
	Index int `json:"Index"`
	Chars int `json:"Chars"`
	// EngineElapsed is synthesis alone. SynthElapsed also covers waiting
	// behind whatever was already playing, so it is the wrong numerator for a
	// realtime factor on any segment after the first.
	EngineElapsed int64   `json:"EngineElapsed"`
	SynthElapsed  int64   `json:"SynthElapsed"`
	AudioSeconds  float64 `json:"AudioSeconds"`
	PlayElapsed   int64   `json:"PlayElapsed"`
	Fallback      bool    `json:"Fallback"`
	Interrupted   bool    `json:"Interrupted"`
}

// engineNanos is the synthesis wall, falling back to the total for records
// written before the split existed.
func (s rawSegment) engineNanos() int64 {
	if s.EngineElapsed > 0 {
		return s.EngineElapsed
	}
	return s.SynthElapsed
}

type rawError struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// engine is the grouping key: a provider and its model tier. The tier is part
// of the identity because the same provider measured 1.33x realtime at 0.6b
// and 1.87x at 1.7b, and an artifact that pooled them would describe neither.
func (r rawResult) engine() string {
	if r.Metrics.TTSProvider == "" {
		return ""
	}
	if r.Metrics.TTSTier == "" {
		return r.Metrics.TTSProvider
	}
	return r.Metrics.TTSProvider + ":" + r.Metrics.TTSTier
}

// producedAudio reports whether the turn ever became audible. PlaybackStart is
// stamped only when a segment actually started playing, so zero means silence
// — a turn that produced no audio must not contribute latency percentiles, or
// a provider that mostly failed reads as the fast one.
func (r rawResult) producedAudio() bool { return r.Metrics.PlaybackStartElapsed > 0 }

// usedFallback reports whether any segment of this turn was spoken by the
// fallback engine instead of the configured one.
//
// Such a turn is excluded from every latency figure. TurnMetrics.TTSProvider
// names the engine that was CONFIGURED, stamped before synthesis begins, so a
// turn that fell back still carries the primary's name: aggregating it would
// credit the primary with the fallback's timings. Kokoro is roughly four
// times faster than qwen3-tts:1.7b here, so a handful of contaminated turns
// could pull a candidate's median under its control's and reverse the
// decision the artifact exists to record.
//
// The exclusion is deliberately whole-turn rather than per-segment even
// though a turn whose LAST segment fell back still measured its first audio
// cleanly. Turn-level figures like dead air span the whole reply and cannot
// be attributed segment by segment, and a column that mixes engines is worth
// less than one with fewer, honest samples.
func (r rawResult) usedFallback() bool {
	if r.Metrics.TTSFallbacks > 0 {
		return true
	}
	for _, seg := range r.Metrics.Segments {
		if seg.Fallback {
			return true
		}
	}
	return false
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }

// EngineStats is one engine's column in the artifact.
type EngineStats struct {
	Turns             int      `json:"turns"`
	Fixtures          int      `json:"fixtures"`
	Iterations        int      `json:"iterations"`
	RequestedAs       []string `json:"requested_as"`
	TurnsWithoutAudio int      `json:"turns_without_audio"`
	TurnsWithErrors   int      `json:"turns_with_errors,omitempty"`
	// TurnsWithFallback counts turns in which the configured provider failed
	// at least one segment and Kokoro spoke it instead. Those turns are not a
	// measurement of the engine named in the column, so they are excluded
	// from every latency figure below and reported only as this count.
	TurnsWithFallback int `json:"turns_with_fallback"`
	// SegmentsFallback counts segments the fallback engine spoke. They are
	// excluded from the segment aggregates for the same reason.
	SegmentsFallback int `json:"segments_fallback"`
	// TurnsStallWatchdogFired counts turns the playback-stall watchdog cut
	// short. Reported beside the grace it ran under, because a turn slower
	// than the grace and a hung one are the same observation until it fires.
	TurnsStallWatchdogFired int      `json:"turns_stall_watchdog_fired"`
	FirstAudioGraceMS       *float64 `json:"first_audio_grace_ms"`

	// SynthStage is first segment handed to TTS -> first audible frame. It is
	// the TTS-only stage: it excludes model think time, which no TTS change
	// can move.
	MedianSynthStageMS *float64 `json:"median_synth_stage_ms"`
	P90SynthStageMS    *float64 `json:"p90_synth_stage_ms"`

	// FirstChunkToAudio and ModelCompleteToAudio are the two readings of
	// "how long after the model answers do I hear something". The second is
	// negative when the progressive path works — audio starts before the
	// reply finishes — so a positive median there means the pipeline waited
	// for the whole reply before synthesizing.
	MedianFirstChunkToAudioMS    *float64 `json:"median_first_chunk_to_audio_ms"`
	MedianModelCompleteToAudioMS *float64 `json:"median_model_complete_to_audio_ms"`

	MedianPlaybackStartMS *float64 `json:"median_playback_start_ms"`
	P90PlaybackStartMS    *float64 `json:"p90_playback_start_ms"`

	// DeadAir is silence between sentences: the player idle after one segment
	// finished and before the next was ready. A whole-utterance engine slower
	// than realtime cannot get ahead of playback, so the gap recurs at every
	// sentence boundary and no watchdog sees it — the stall watchdog only
	// checks the first audible frame.
	MedianDeadAirMS    *float64 `json:"median_dead_air_ms"`
	MedianDeadAirShare *float64 `json:"median_dead_air_share_of_speaking"`

	// Segment-level aggregates. Segments, not turns, are the population here:
	// a turn's first segment and its fifth are separate synthesis calls and
	// pooling them into one per-turn figure hides where a long reply spends
	// its time.
	Segments              int      `json:"segments"`
	MedianSegmentsPerTurn *float64 `json:"median_segments_per_turn"`
	MedianSegmentEngineMS *float64 `json:"median_segment_engine_ms"`
	MedianSegmentSynthMS  *float64 `json:"median_segment_synth_ms"`
	// MedianSegmentQueueMS is the wait behind earlier playback. It is
	// reported so a large synth figure can be attributed: a segment queued
	// behind a long sentence is not evidence of a slow engine.
	MedianSegmentQueueMS *float64 `json:"median_segment_queue_ms"`
	MedianSegmentAudioS  *float64 `json:"median_segment_audio_s"`
	// RealtimeFactor is synth wall / audio duration. Above 1.0 the engine
	// produces audio more slowly than the speaker consumes it.
	MedianRealtimeFactor *float64 `json:"median_realtime_factor"`
	P90RealtimeFactor    *float64 `json:"p90_realtime_factor"`
	// FirstSegmentRTF is broken out because time-to-first-audio waits on that
	// segment alone, and it is the one a batching decision hinges on.
	MedianFirstSegmentRTF *float64 `json:"median_first_segment_rtf"`
	SegmentsInterrupted   int      `json:"segments_interrupted"`
}

// Result is the decision block. The *_criterion_met verdicts stay null:
// "met" is a judgement against criteria fixed before the numbers existed,
// which arithmetic cannot assert.
type Result struct {
	SynthStageDeltaMS       *float64 `json:"synth_stage_delta_ms"`
	DeadAirDeltaPoints      *float64 `json:"dead_air_delta_points"`
	RealtimeFactorDelta     *float64 `json:"realtime_factor_delta"`
	LatencyCriterionMet     *bool    `json:"latency_criterion_met"`
	NaturalnessCriterionMet *bool    `json:"naturalness_criterion_met"`
	ReliabilityCriterionMet *bool    `json:"reliability_criterion_met"`
	// AudioRegressed is the failure that must not hide behind a good median:
	// a candidate that produced audio on fewer turns than the control.
	AudioRegressed bool `json:"audio_regressed"`
	// ClearsBatchingThreshold reports whether the candidate's median realtime
	// factor beats the ~2.3x needed for cross-sentence batching (festival
	// decision D009). Nil when it was not measured.
	ClearsBatchingThreshold *bool  `json:"clears_batching_threshold"`
	Decision                string `json:"decision"`
}

// batchingRealtimeFactor is the threshold from D009 / TestKokoroRealtimeFactor:
// batching two sentences into one call is only free when
// synth(2 sentences) < playback(1 sentence), which needs the synthesizer
// running about 2.3x faster than realtime. Expressed here as an RTF, so
// smaller wins: 1/2.3.
const batchingRealtimeFactor = 1.0 / 2.3

// Report is the full artifact, shaped to be committed to docs/benchmarks/.
type Report struct {
	Schema                 string                 `json:"schema"`
	Item                   string                 `json:"item"`
	Date                   string                 `json:"date"`
	Question               string                 `json:"question"`
	CriteriaFixedInAdvance map[string]string      `json:"criteria_fixed_in_advance"`
	Control                string                 `json:"control"`
	Candidate              string                 `json:"candidate"`
	Engines                map[string]EngineStats `json:"engines"`
	Result                 Result                 `json:"result"`
	PercentileMethod       string                 `json:"percentile_method"`
	WarmupDropped          bool                   `json:"warmup_dropped"`
	ReadingTheNumbers      string                 `json:"reading_the_numbers"`
	EvidenceLimits         string                 `json:"evidence_limits"`
	DurableValue           string                 `json:"durable_value"`
	Provenance             Provenance             `json:"provenance"`
}

// Provenance carries the keys L4 uses, plus the brain model: TTS latency is
// measured inside a whole turn, and the same code under a 135M model and a 26B
// one produces entirely different turn timings.
type Provenance struct {
	ObeyVoiceCommit string `json:"obey_voice_commit"`
	Machine         string `json:"machine"`
	CPU             string `json:"cpu"`
	OS              string `json:"os"`
	Backend         string `json:"backend,omitempty"`
	BrainProvider   string `json:"brain_provider,omitempty"`
	BrainModel      string `json:"brain_model,omitempty"`
	Persona         string `json:"persona,omitempty"`
	Fixtures        string `json:"fixtures"`
	Iterations      int    `json:"iterations"`
	Command         string `json:"command"`
	Aggregator      string `json:"aggregator"`
}

// benchmarkEnvelope is the shape scripts/bench.sh writes: the raw array
// wrapped with provenance and thresholds. A fresh --json-file run is a bare
// array, so both have to be readable.
type benchmarkEnvelope struct {
	Results []rawResult `json:"results"`
}

// ParseResults decodes either a bare `obey-voice benchmark --json-file` array
// or a bench.sh envelope.
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
// latency medians as a warm-up run. It matters more here than for STT: a cold
// Qwen worker pays 7-11s of model load on its first synthesis, which would
// dominate any median it entered.
func DropWarmup(results []rawResult) []rawResult {
	kept := make([]rawResult, 0, len(results))
	for _, r := range results {
		if r.Iteration > 1 {
			kept = append(kept, r)
		}
	}
	return kept
}

// TurnsByEngine keeps turns that reported a TTS engine, grouped by it.
//
// Grouping on what the pipeline reported, not on --tts-provider, is
// deliberate: a leg that was asked for qwen3-tts and silently ran Kokoro is
// a Kokoro measurement, and the requested value is preserved separately so
// that substitution is visible rather than averaged in.
func TurnsByEngine(results []rawResult) map[string][]rawResult {
	byEngine := make(map[string][]rawResult)
	for _, r := range results {
		if r.Mode == "stt" {
			continue
		}
		key := r.engine()
		if key == "" {
			continue
		}
		byEngine[key] = append(byEngine[key], r)
	}
	return byEngine
}

// StatsFor aggregates one engine's turns.
func StatsFor(runs []rawResult) EngineStats {
	stats := EngineStats{Turns: len(runs)}

	fixtures := make(map[string]struct{})
	requested := make(map[string]struct{})
	graces := make(map[float64]struct{})

	var synthStage, firstChunkToAudio, modelCompleteToAudio, playbackStart []float64
	var segmentsPerTurn, segEngine, segSynth, segQueue, segAudio, segRTF, firstSegRTF []float64
	var deadAir, deadAirShare []float64

	for _, r := range runs {
		if r.Fixture != "" {
			fixtures[r.Fixture] = struct{}{}
		} else if r.Prompt != "" {
			fixtures[r.Prompt] = struct{}{}
		}
		if r.Iteration > stats.Iterations {
			stats.Iterations = r.Iteration
		}
		if r.TTSRequested != "" {
			requested[r.TTSRequested] = struct{}{}
		}
		if len(r.Errors) > 0 {
			stats.TurnsWithErrors++
		}
		fellBack := r.usedFallback()
		if fellBack {
			stats.TurnsWithFallback++
		}
		if r.Metrics.StallWatchdogFired {
			stats.TurnsStallWatchdogFired++
		}
		if r.Metrics.FirstAudioGrace > 0 {
			graces[ms(r.Metrics.FirstAudioGrace)] = struct{}{}
		}

		if !r.producedAudio() {
			stats.TurnsWithoutAudio++
		} else if !fellBack {
			m := r.Metrics
			if m.FirstSegmentElapsed > 0 && m.FirstAudioReadyElapsed > 0 {
				synthStage = append(synthStage, ms(m.FirstAudioReadyElapsed-m.FirstSegmentElapsed))
			}
			if m.FirstModelChunkElapsed > 0 {
				firstChunkToAudio = append(firstChunkToAudio, ms(m.PlaybackStartElapsed-m.FirstModelChunkElapsed))
			}
			if m.ModelCompleteElapsed > 0 {
				// Negative is the healthy case: audio started before the reply
				// finished. Kept signed rather than clamped, because clamping
				// would erase exactly the distinction this figure exists for.
				modelCompleteToAudio = append(modelCompleteToAudio, ms(m.PlaybackStartElapsed-m.ModelCompleteElapsed))
			}
			playbackStart = append(playbackStart, ms(m.PlaybackStartElapsed))
			deadAir = append(deadAir, ms(m.DeadAirElapsed))
			if speaking := m.PlaybackCompleteElapsed - m.PlaybackStartElapsed; speaking > 0 {
				deadAirShare = append(deadAirShare, float64(m.DeadAirElapsed)/float64(speaking))
			}
		}

		if fellBack {
			// Its segments are counted below so the exclusion is visible, but
			// none of this turn's timings describe the engine in this column.
			for _, seg := range r.Metrics.Segments {
				if seg.Fallback {
					stats.SegmentsFallback++
				}
			}
			continue
		}

		segmentsPerTurn = append(segmentsPerTurn, float64(len(r.Metrics.Segments)))
		for _, seg := range r.Metrics.Segments {
			if seg.Fallback {
				stats.SegmentsFallback++
				continue
			}
			stats.Segments++
			if seg.Interrupted {
				stats.SegmentsInterrupted++
			}
			segEngine = append(segEngine, ms(seg.engineNanos()))
			segSynth = append(segSynth, ms(seg.SynthElapsed))
			if wait := seg.SynthElapsed - seg.engineNanos(); wait > 0 {
				segQueue = append(segQueue, ms(wait))
			} else {
				segQueue = append(segQueue, 0)
			}
			segAudio = append(segAudio, seg.AudioSeconds)
			if seg.AudioSeconds > 0 {
				rtf := ms(seg.engineNanos()) / 1000 / seg.AudioSeconds
				segRTF = append(segRTF, rtf)
				if seg.Index == 0 {
					firstSegRTF = append(firstSegRTF, rtf)
				}
			}
		}
	}

	stats.Fixtures = len(fixtures)
	stats.RequestedAs = sortedKeys(requested)
	// One grace per engine is expected; more than one means the sweep mixed
	// configurations, so report nothing rather than a figure that is true of
	// only some turns.
	if len(graces) == 1 {
		for g := range graces {
			stats.FirstAudioGraceMS = &g
		}
	}

	stats.MedianSynthStageMS = roundPtr(percentile(synthStage, 50), 1)
	stats.P90SynthStageMS = roundPtr(percentile(synthStage, 90), 1)
	stats.MedianFirstChunkToAudioMS = roundPtr(percentile(firstChunkToAudio, 50), 1)
	stats.MedianModelCompleteToAudioMS = roundPtr(percentile(modelCompleteToAudio, 50), 1)
	stats.MedianPlaybackStartMS = roundPtr(percentile(playbackStart, 50), 1)
	stats.MedianDeadAirMS = roundPtr(percentile(deadAir, 50), 1)
	stats.MedianDeadAirShare = roundPtr(percentile(deadAirShare, 50), 4)
	stats.P90PlaybackStartMS = roundPtr(percentile(playbackStart, 90), 1)
	stats.MedianSegmentsPerTurn = roundPtr(percentile(segmentsPerTurn, 50), 1)
	stats.MedianSegmentEngineMS = roundPtr(percentile(segEngine, 50), 1)
	stats.MedianSegmentSynthMS = roundPtr(percentile(segSynth, 50), 1)
	stats.MedianSegmentQueueMS = roundPtr(percentile(segQueue, 50), 1)
	stats.MedianSegmentAudioS = roundPtr(percentile(segAudio, 50), 3)
	stats.MedianRealtimeFactor = roundPtr(percentile(segRTF, 50), 4)
	stats.P90RealtimeFactor = roundPtr(percentile(segRTF, 90), 4)
	stats.MedianFirstSegmentRTF = roundPtr(percentile(firstSegRTF, 50), 4)
	return stats
}

// Compare fills the decision block's arithmetic. Prose and criterion verdicts
// stay for a human.
func Compare(control, candidate EngineStats) Result {
	var res Result
	if control.MedianSynthStageMS != nil && candidate.MedianSynthStageMS != nil {
		delta := *candidate.MedianSynthStageMS - *control.MedianSynthStageMS
		res.SynthStageDeltaMS = roundPtr(&delta, 1)
	}
	if control.MedianRealtimeFactor != nil && candidate.MedianRealtimeFactor != nil {
		delta := *candidate.MedianRealtimeFactor - *control.MedianRealtimeFactor
		res.RealtimeFactorDelta = roundPtr(&delta, 4)
	}
	if candidate.MedianRealtimeFactor != nil {
		clears := *candidate.MedianRealtimeFactor <= batchingRealtimeFactor
		res.ClearsBatchingThreshold = &clears
	}
	res.AudioRegressed = candidate.TurnsWithoutAudio > control.TurnsWithoutAudio
	if control.MedianDeadAirShare != nil && candidate.MedianDeadAirShare != nil {
		delta := (*candidate.MedianDeadAirShare - *control.MedianDeadAirShare) * 100
		res.DeadAirDeltaPoints = roundPtr(&delta, 1)
	}
	return res
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
