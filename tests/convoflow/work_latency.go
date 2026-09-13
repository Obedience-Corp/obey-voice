package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const workLatencySchema = "obey-voice.convoflow-latency/v1"

type workLatencySample struct {
	TTSProvider    string        `json:"tts_provider"`
	TTSTier        string        `json:"tts_tier"`
	TTSWarm        bool          `json:"tts_warm"`
	TTSFallbacks   int           `json:"tts_fallbacks"`
	Condition      string        `json:"condition"`
	Fixture        string        `json:"fixture"`
	Iteration      int           `json:"iteration"`
	Warmup         bool          `json:"warmup"`
	Provenance     string        `json:"provenance"`
	Input          string        `json:"input"`
	Sink           string        `json:"sink"`
	WorkersAtStart int           `json:"workers_at_start"`
	PendingAtStart int           `json:"pending_at_start"`
	StartedAt      time.Time     `json:"started_at"`
	Elapsed        time.Duration `json:"elapsed_ns"`
	PlaybackStart  time.Duration `json:"playback_start_ns"`
	STTFinal       time.Duration `json:"stt_final_ns"`
	FirstAudio     time.Duration `json:"first_audio_ns"`
	FirstModel     time.Duration `json:"first_model_ns"`
	Error          string        `json:"error,omitempty"`
}
type workLatencyReport struct {
	Schema     string                        `json:"schema"`
	Samples    []workLatencySample           `json:"samples"`
	Conditions map[string]workLatencySummary `json:"conditions,omitempty"`
	Notes      []string                      `json:"notes,omitempty"`
}
type workLatencySummary struct {
	Samples     int           `json:"samples"`
	P50Response time.Duration `json:"p50_final_to_playback_ns"`
	P95Response time.Duration `json:"p95_final_to_playback_ns"`
}

// collectLatency uses the production turn's monotonic duration fields, never
// serialized wall-clock subtraction. A text turn cannot masquerade as STT.
func (e *engine) collectLatency(turn Turn, start time.Time, mark int, before work.Snapshot, runErr error) error {
	if e.scn.LatencyCondition == "" || turn.LatencyIteration == 0 {
		return nil
	}
	s := workLatencySample{Condition: e.scn.LatencyCondition, Iteration: turn.LatencyIteration, Warmup: turn.LatencyWarmup, Provenance: e.latencyProvenance, Input: e.scn.Input, StartedAt: start, Elapsed: time.Since(start), PendingAtStart: len(before.Pending)}
	if e.scn.Capture.Microphone {
		s.Input = "microphone"
	}
	for _, j := range before.Jobs {
		if j.State == work.JobRunning {
			s.WorkersAtStart++
		}
	}
	var errs []error
	if runErr != nil {
		errs = append(errs, runErr)
	}
	if turn.WAV == "" {
		errs = append(errs, errors.New("measured turn requires recorded WAV"))
	} else if data, err := os.ReadFile(turn.WAV); err != nil {
		errs = append(errs, err)
	} else {
		hash := sha256.Sum256(data)
		s.Fixture = hex.EncodeToString(hash[:])
	}
	count := 0
	for _, te := range e.rec.snapshot()[mark:] {
		if m, ok := te.event.(events.TurnMetrics); ok {
			count++
			s.TTSProvider = m.TTSProvider
			s.TTSTier = m.TTSTier
			s.TTSWarm = m.TTSWarm
			s.TTSFallbacks = m.TTSFallbacks
			s.STTFinal = m.STTFinalElapsed
			s.PlaybackStart = m.PlaybackStartElapsed
			s.FirstAudio = m.FirstAudioReadyElapsed
			s.FirstModel = m.FirstModelChunkElapsed
			s.Sink = m.AudibleSink
			if m.Outcome != "completed" || m.Degraded || m.Interrupted {
				errs = append(errs, fmt.Errorf("turn outcome %q degraded=%t interrupted=%t", m.Outcome, m.Degraded, m.Interrupted))
			}
		}
	}
	if count != 1 {
		errs = append(errs, fmt.Errorf("expected one production turn metrics record, got %d", count))
	}
	if err := errors.Join(errs...); err != nil {
		s.Error = err.Error()
	}
	e.latencySamples = append(e.latencySamples, s)
	return errors.Join(errs...)
}

func validateWorkLatency(samples []workLatencySample) (workLatencyReport, error) {
	out := workLatencyReport{Schema: workLatencySchema, Samples: append([]workLatencySample(nil), samples...), Conditions: map[string]workLatencySummary{}, Notes: []string{"Measures finalized recorded input to local playback start. Physical microphone runs retain separate acoustic evidence. Safe-yield and barge-in acceptance remain separate.", "Warmup is an explicit label, not evidence of a cold model. Shared and separate endpoints must be collected as separate pairs."}}
	seen := map[string]bool{}
	groups := map[string][]time.Duration{}
	provenance := ""
	engine := ""
	input := ""
	for _, s := range samples {
		if s.Condition != "baseline" && s.Condition != "worker" {
			return out, fmt.Errorf("latency: invalid condition %q", s.Condition)
		}
		if s.Error != "" {
			return out, fmt.Errorf("latency: failed sample %s: %s", s.Fixture, s.Error)
		}
		if s.TTSProvider == "" || s.TTSFallbacks != 0 {
			return out, fmt.Errorf("latency: missing TTS identity or fallback (provider=%q fallbacks=%d)", s.TTSProvider, s.TTSFallbacks)
		}
		actual := s.TTSProvider + "/" + s.TTSTier
		if engine != "" && engine != actual {
			return out, errors.New("latency: mixed actual TTS providers/tiers")
		}
		engine = actual
		if (s.Input != "speech" && s.Input != "microphone") || s.Sink != "local_speaker" {
			return out, fmt.Errorf("latency: recorded speech with local speaker observation required (input=%q sink=%q)", s.Input, s.Sink)
		}
		if input != "" && input != s.Input {
			return out, errors.New("latency: mixed recorded and physical microphone inputs")
		}
		input = s.Input
		if s.Fixture == "" || s.Iteration < 1 || s.Provenance == "" {
			return out, errors.New("latency: missing fixture, iteration or provenance")
		}
		if provenance != "" && provenance != s.Provenance {
			return out, errors.New("latency: mismatched provenance across paired samples")
		}
		provenance = s.Provenance
		key := fmt.Sprintf("%s/%s/%d", s.Condition, s.Fixture, s.Iteration)
		if seen[key] {
			return out, fmt.Errorf("latency: duplicate sample %s", key)
		}
		seen[key] = true
		if s.STTFinal <= 0 || s.FirstModel < s.STTFinal || s.FirstAudio < s.FirstModel || s.PlaybackStart < s.FirstAudio || s.PlaybackStart <= s.STTFinal || s.Elapsed < s.PlaybackStart {
			return out, fmt.Errorf("latency: invalid STT/audio/playback ordering for %s", key)
		}
		if s.PendingAtStart != 0 || (s.Condition == "baseline" && s.WorkersAtStart != 0) || (s.Condition == "worker" && s.WorkersAtStart < 1) {
			return out, fmt.Errorf("latency: workload does not match %s (running=%d pending=%d)", s.Condition, s.WorkersAtStart, s.PendingAtStart)
		}
		if !s.Warmup {
			groups[s.Condition] = append(groups[s.Condition], s.PlaybackStart-s.STTFinal)
		}
	}
	populations := map[string]map[string]bool{"baseline": {}, "worker": {}}
	for _, s := range samples {
		populations[s.Condition][fmt.Sprintf("%s/%d/%t", s.Fixture, s.Iteration, s.Warmup)] = true
	}
	if len(populations["baseline"]) != len(populations["worker"]) {
		return out, errors.New("latency: baseline/worker fixture populations differ")
	}
	for k := range populations["baseline"] {
		if !populations["worker"][k] {
			return out, fmt.Errorf("latency: missing matching worker sample %s", k)
		}
	}
	for _, condition := range []string{"baseline", "worker"} {
		v := groups[condition]
		if len(v) < 30 {
			return out, fmt.Errorf("latency: %s has %d samples; need at least 30", condition, len(v))
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		out.Conditions[condition] = workLatencySummary{len(v), percentile(v, .5), percentile(v, .95)}
	}
	return out, nil
}

// Nearest-rank percentile, with a documented rank for an even population.
func percentile(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	i := int(math.Ceil(float64(len(v))*p)) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(v) {
		i = len(v) - 1
	}
	return v[i]
}
func compareWorkLatencyFiles(left, right string) (workLatencyReport, error) {
	var samples []workLatencySample
	for _, path := range []string{left, right} {
		f, err := os.Open(path)
		if err != nil {
			return workLatencyReport{}, err
		}
		var m metricsFile
		err = json.NewDecoder(f).Decode(&m)
		_ = f.Close()
		if err != nil {
			return workLatencyReport{}, err
		}
		if m.Latency == nil || m.Latency.Schema != workLatencySchema {
			return workLatencyReport{}, fmt.Errorf("latency: missing/unsupported report in %s", path)
		}
		samples = append(samples, m.Latency.Samples...)
		if m.StopReason != "completed" {
			return workLatencyReport{Schema: workLatencySchema, Samples: samples}, fmt.Errorf("latency: run %s did not complete (%s)", path, m.StopReason)
		}
	}
	return validateWorkLatency(samples)
}
