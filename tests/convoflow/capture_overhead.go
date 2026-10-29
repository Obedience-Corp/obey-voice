package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Capture overhead is a within-population recording off/on comparison. It is
// deliberately separate from the existing baseline-versus-worker comparison.
type captureArm struct {
	Pair    int  `json:"pair"`
	Warmup  bool `json:"warmup"`
	Capture bool `json:"capture"`
}
type captureObservation struct {
	QuietOutput  bool                     `json:"quiet_output_verified"`
	OutputDevice audio.NegotiatedDevice   `json:"output_device"`
	Gain         float64                  `json:"playback_gain"`
	Type         string                   `json:"type"`
	Population   *captureArm              `json:"population"`
	Valid        bool                     `json:"population_valid"`
	Capture      bool                     `json:"capture"`
	Available    bool                     `json:"metrics_available"`
	CapPossible  bool                     `json:"configured_token_cap_possible"`
	Outcome      string                   `json:"outcome"`
	Error        string                   `json:"error"`
	Sink         string                   `json:"audible_sink"`
	Timing       events.TurnTiming        `json:"timing"`
	Trace        *events.TraceContext     `json:"trace"`
	Envelope     *events.ResponseEnvelope `json:"envelope"`
	Transcript   string                   `json:"transcript"`
	Message      string                   `json:"message"`
	WorkEnabled  bool                     `json:"work_enabled"`
}
type capturePair struct {
	Pair    int      `json:"pair"`
	Order   []string `json:"order"`
	OffMS   float64  `json:"off_ms"`
	OnMS    float64  `json:"on_ms"`
	DeltaMS float64  `json:"on_minus_off_ms"`
}
type captureDistribution struct {
	N        int     `json:"n"`
	MinMS    float64 `json:"min_ms"`
	MedianMS float64 `json:"p50_ms"`
	P95MS    float64 `json:"p95_ms"`
	MaxMS    float64 `json:"max_ms"`
	MeanMS   float64 `json:"mean_ms"`
	SDMS     float64 `json:"sample_sd_ms"`
}
type captureOverheadReport struct {
	Decision        string              `json:"decision"`
	WorkEnabled     bool                `json:"work_enabled"`
	Measurement     string              `json:"measurement"`
	CostScope       string              `json:"cost_scope"`
	Provenance      map[string]any      `json:"provenance"`
	Seed            int64               `json:"population_seed"`
	RawRows         int                 `json:"raw_rows"`
	WarmupRows      int                 `json:"excluded_predeclared_warmup_rows"`
	UnassignedRows  int                 `json:"unassigned_shutdown_or_control_rows"`
	Off             captureDistribution `json:"capture_off"`
	On              captureDistribution `json:"capture_on"`
	PairedDelta     captureDistribution `json:"paired_delta"`
	P95RegressionMS float64             `json:"p95_on_minus_off_ms"`
	BudgetMS        float64             `json:"budget_ms"`
	CI95            []float64           `json:"paired_bootstrap_p95_regression_ci95_ms"`
	Bootstrap       string              `json:"uncertainty_method"`
	Samples         []capturePair       `json:"pairs"`
	Diagnostics     map[string]int      `json:"diagnostic_counts"`
	Problems        []string            `json:"problems,omitempty"`
}

func captureQuantile(values []float64, p float64) float64 {
	durations := make([]time.Duration, len(values))
	for i, value := range values {
		durations[i] = time.Duration(value * float64(time.Millisecond))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return float64(percentile(durations, p)) / float64(time.Millisecond)
}
func captureStats(values []float64) captureDistribution {
	result := captureDistribution{N: len(values)}
	if len(values) == 0 {
		return result
	}
	result.MinMS, result.MaxMS = values[0], values[0]
	for _, value := range values {
		result.MinMS = min(result.MinMS, value)
		result.MaxMS = max(result.MaxMS, value)
		result.MeanMS += value
	}
	result.MeanMS /= float64(len(values))
	for _, value := range values {
		result.SDMS += (value - result.MeanMS) * (value - result.MeanMS)
	}
	if len(values) > 1 {
		result.SDMS = math.Sqrt(result.SDMS / float64(len(values)-1))
	}
	result.MedianMS = captureQuantile(values, .5)
	result.P95MS = captureQuantile(values, .95)
	return result
}
func readCaptureJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	// These bounded metadata objects may be pretty printed across lines.
	var raw []byte
	for scanner.Scan() {
		if len(raw)+len(scanner.Bytes())+1 > 1<<20 {
			return fmt.Errorf("metadata exceeds1MiB")
		}
		raw = append(raw, scanner.Bytes()...)
		raw = append(raw, '\n')
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func analyzeCaptureOverhead(root string) (captureOverheadReport, error) {
	report := captureOverheadReport{Decision: "invalid", Measurement: "finalized_input → first local software playback (milliseconds)", CostScope: "incremental recording on the same instrumented binary; not pre-feature instrumentation cost", Diagnostics: map[string]int{}, Bootstrap: "paired percentile bootstrap;10000 resamples;seed7007;nearest-rank quantiles"}
	fail := func(message string) (captureOverheadReport, error) {
		report.Problems = append(report.Problems, message)
		return report, fmt.Errorf("capture overhead: %s", message)
	}
	var plan struct {
		Seed    int64        `json:"seed"`
		Entries []captureArm `json:"entries"`
	}
	if err := readCaptureJSON(filepath.Join(root, "population-plan.json"), &plan); err != nil {
		return fail(err.Error())
	}
	report.Seed = plan.Seed
	if len(plan.Entries) < 62 || len(plan.Entries) > 242 {
		return fail("requires30..120 predefined paired samples plus two warmups")
	}
	var metadata map[string]any
	if err := readCaptureJSON(filepath.Join(root, "provider-provenance.json"), &metadata); err != nil {
		return fail(err.Error())
	}
	for _, key := range []string{"binary_sha256", "fixture_config_sha256", "wav_sha256", "brain_model", "stt_model", "tts_provider", "source_revision"} {
		value, ok := metadata[key].(string)
		if !ok || value == "" {
			return fail("missing provenance " + key)
		}
	}
	if silent, ok := metadata["silent_output"].(bool); !ok || !silent {
		return fail("requires predeclared quiet native-output profile; do not mix earlier physical datasets")
	}
	requested, ok := metadata["requested_output_device"].(string)
	if !ok || requested == "" {
		return fail("missing named quiet output device")
	}
	if gain, ok := metadata["playback_gain"].(float64); !ok || gain != 0 {
		return fail("quiet profile gain must be0")
	}
	report.Provenance = metadata
	enabled, ok := metadata["work_enabled"].(bool)
	if !ok {
		return fail("missing work-enabled provenance")
	}
	report.WorkEnabled = enabled
	file, err := os.Open(filepath.Join(root, "observations.jsonl"))
	if err != nil {
		return fail(err.Error())
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	pairs := map[int]*capturePair{}
	seen := map[int]map[bool]bool{}
	plannedIndex := 0
	prompt, transcript := "", ""
	turnIDs, responseIDs := map[string]bool{}, map[string]bool{}
	runtime, session, epoch := "", "", ""
	for scanner.Scan() {
		report.RawRows++
		var row captureObservation
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return fail("malformed raw observation: " + err.Error())
		}
		if row.Type == "diagnostic" || row.Type == "diagnostic_error" {
			if len(report.Diagnostics) < 16 {
				key := row.Message
				if len(key) > 160 {
					key = key[:160] + "…"
				}
				report.Diagnostics[key]++
			}
			continue
		}
		if row.Type != "turn" {
			continue
		}
		if row.Population == nil {
			report.UnassignedRows++
			continue
		}
		if plannedIndex >= len(plan.Entries) || *row.Population != plan.Entries[plannedIndex] {
			return fail("raw planned attempt order differs from predeclared population")
		}
		plannedIndex++
		value := row.Timing.Normalized.PlaybackStartMS
		if !row.QuietOutput || row.Gain != 0 || !row.OutputDevice.IdentityKnown || row.OutputDevice.Name != requested || row.OutputDevice.RequestedName != requested || !row.Valid || !row.Available || row.Outcome != "completed" || row.Error != "" || row.CapPossible || row.Sink != "local_speaker" || row.Timing.Origin != "finalized_input" || value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value >= float64(math.MaxInt64)/float64(time.Millisecond) || row.Capture != row.Population.Capture || row.WorkEnabled != report.WorkEnabled || row.Trace == nil || row.Trace.TurnID == nil || row.Trace.ResponseID == nil {
			return fail(fmt.Sprintf("failed/missing/contradictory planned attempt%d retained; no silent resampling", plannedIndex))
		}
		if row.Envelope == nil || row.Envelope.PromptHash == "" {
			return fail("missing frozen prompt provenance")
		}
		if *row.Trace.TurnID == "" || *row.Trace.ResponseID == "" || turnIDs[*row.Trace.TurnID] || responseIDs[*row.Trace.ResponseID] {
			return fail("reused/missing invocation or response identity")
		}
		turnIDs[*row.Trace.TurnID] = true
		responseIDs[*row.Trace.ResponseID] = true
		if runtime == "" {
			runtime, session, epoch = row.Trace.RuntimeID, row.Trace.SessionID, row.Trace.ConversationEpoch
		}
		if runtime == "" || row.Trace.RuntimeID != runtime || row.Trace.SessionID != session || row.Trace.ConversationEpoch != epoch || row.Envelope.ResponseID != *row.Trace.ResponseID {
			return fail("mixed immutable source scope/response identity")
		}
		if row.Timing.Raw.FinalizedInputMS == nil || row.Timing.Raw.PlaybackStartMS == nil || math.Abs((*row.Timing.Raw.PlaybackStartMS-*row.Timing.Raw.FinalizedInputMS)-*value) > 1 {
			return fail("raw/normalized finalized-input playback reference disagrees")
		}

		if prompt == "" {
			prompt = row.Envelope.PromptHash
			transcript = row.Transcript
		}
		if prompt != row.Envelope.PromptHash || transcript != row.Transcript {
			return fail("effective prompt/transcript changed within population")
		}
		if row.Population.Warmup {
			report.WarmupRows++
			continue
		}
		pairID := row.Population.Pair
		if pairID <= 0 {
			return fail("measured attempt lacks pair identity")
		}
		if seen[pairID] == nil {
			seen[pairID] = map[bool]bool{}
			pairs[pairID] = &capturePair{Pair: pairID}
		}
		if seen[pairID][row.Capture] {
			return fail("duplicate pair arm")
		}
		seen[pairID][row.Capture] = true
		pair := pairs[pairID]
		label := "off"
		if row.Capture {
			label = "on"
			pair.OnMS = *value
		} else {
			pair.OffMS = *value
		}
		pair.Order = append(pair.Order, label)
	}
	if err := scanner.Err(); err != nil {
		return fail(err.Error())
	}
	if plannedIndex != len(plan.Entries) || report.WarmupRows != 2 {
		return fail("population incomplete; raw attempts retained")
	}
	ids := make([]int, 0, len(pairs))
	for id := range pairs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	off, on, deltas := []float64{}, []float64{}, []float64{}
	for _, id := range ids {
		if !seen[id][false] || !seen[id][true] {
			return fail("unmatched pair")
		}
		pair := *pairs[id]
		pair.DeltaMS = pair.OnMS - pair.OffMS
		report.Samples = append(report.Samples, pair)
		off = append(off, pair.OffMS)
		on = append(on, pair.OnMS)
		deltas = append(deltas, pair.DeltaMS)
	}
	if len(ids) < 30 {
		return fail("fewer than30 matched pairs")
	}
	report.Off, report.On, report.PairedDelta = captureStats(off), captureStats(on), captureStats(deltas)
	report.P95RegressionMS = report.On.P95MS - report.Off.P95MS
	report.BudgetMS = math.Max(10, .02*report.Off.P95MS)
	rng := rand.New(rand.NewSource(7007))
	differences := make([]float64, 10000)
	sampleOff, sampleOn := make([]float64, len(ids)), make([]float64, len(ids))
	for iteration := range differences {
		for i := range ids {
			index := rng.Intn(len(ids))
			sampleOff[i], sampleOn[i] = off[index], on[index]
		}
		differences[iteration] = captureQuantile(sampleOn, .95) - captureQuantile(sampleOff, .95)
	}
	report.CI95 = []float64{captureQuantile(differences, .025), captureQuantile(differences, .975)}
	if report.P95RegressionMS <= report.BudgetMS && report.CI95[1] <= report.BudgetMS {
		report.Decision = "pass"
		return report, nil
	}
	report.Decision = "inconclusive"
	if report.CI95[0] > report.BudgetMS {
		report.Decision = "fail"
	}
	return report, fmt.Errorf("capture overhead %s: p95 delta%.3fms budget%.3fms CI[%.3f,%.3f]; retain full population", report.Decision, report.P95RegressionMS, report.BudgetMS, report.CI95[0], report.CI95[1])
}
