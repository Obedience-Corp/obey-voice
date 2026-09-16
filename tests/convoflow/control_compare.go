package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

type controlComparison struct {
	Status             string               `json:"status"`
	Error              string               `json:"error,omitempty"`
	Baseline           controlTimingSummary `json:"baseline_detection_to_stop"`
	Worker             controlTimingSummary `json:"worker_detection_to_stop"`
	P50Delta           time.Duration        `json:"p50_delta_ns"`
	P95Delta           time.Duration        `json:"p95_delta_ns"`
	BaselineMin        time.Duration        `json:"baseline_min_ns"`
	WorkerMax          time.Duration        `json:"worker_max_ns"`
	WorstObservedBound time.Duration        `json:"worst_observed_regression_bound_ns"`
	Allowance          time.Duration        `json:"allowance_ns"`
	Scope              string               `json:"scope"`
}

// Compare raw observations, not the summary's claimed quantiles. The worst
// worker observation minus the smallest baseline gives a conservative bound
// on every cross-condition difference in the measured samples. This avoids
// silently interpreting the contract's 50ms allowance as a p95-only rule.
func compareControlPopulations(baseline, worker string) (report controlComparison, err error) {
	report.Status = "failed"
	report.Allowance = 50 * time.Millisecond
	report.Scope = "Observed detector-to-playback-stop durations only; excludes speech onset/detector delay. Worst worker minus best baseline bounds every observed cross-condition regression. Not a universal latency guarantee or cold-cache claim. Declared runtime asset hashes are required; undeclared environment state is not fingerprinted."
	defer func() {
		if err != nil {
			report.Error = err.Error()
		}
	}()
	a, as, av, err := readComparableControl(baseline, "baseline")
	if err != nil {
		return report, err
	}
	b, bs, bv, err := readComparableControl(worker, "worker")
	if err != nil {
		return report, err
	}
	if a.Samples != b.Samples || a.Warmups != b.Warmups || a.Timeout != b.Timeout || a.Stimulus != b.Stimulus || a.Warmth != b.Warmth || a.ControlSpec != b.ControlSpec || a.ScenarioSpec != b.ScenarioSpec || !maps.Equal(a.Roles, b.Roles) || !maps.Equal(a.Runtime, b.Runtime) {
		return report, errors.New("control populations differ in sample plan, pinned binary/config/fixtures, runtime assets or stimulus route")
	}
	if err := compareControlIdentity(as, bs); err != nil {
		return report, err
	}
	report.Baseline = summarizeControlTimes(av)
	report.Worker = summarizeControlTimes(bv)
	report.P50Delta = report.Worker.P50 - report.Baseline.P50
	report.P95Delta = report.Worker.P95 - report.Baseline.P95
	sort.Slice(av, func(i, j int) bool { return av[i] < av[j] })
	sort.Slice(bv, func(i, j int) bool { return bv[i] < bv[j] })
	report.BaselineMin, report.WorkerMax = av[0], bv[len(bv)-1]
	report.WorstObservedBound = report.WorkerMax - report.BaselineMin
	if report.WorstObservedBound > report.Allowance {
		return report, errors.New("observed interruption regression bound exceeds 50ms")
	}
	report.Status = "passed"
	return report, nil
}

func readComparableControl(dir, condition string) (controlPopulationPlan, controlPopulationSample, []time.Duration, error) {
	var plan controlPopulationPlan
	var summary controlPopulationSummary
	var first controlPopulationSample
	hashes := make(map[string]string)
	if err := readControlJSON(filepath.Join(dir, "plan.json"), &plan, hashes); err != nil {
		return plan, first, nil, err
	}
	if err := readControlJSON(filepath.Join(dir, "summary.json"), &summary, hashes); err != nil {
		return plan, first, nil, err
	}
	if plan.Schema != "acoustic-control-population.v1" || plan.Operation != "acoustic_interrupt" || plan.Condition != condition || plan.Samples < 30 || plan.Warmups < 0 || plan.Timeout <= 0 || summary.Status != "completed" || summary.Error != "" || summary.Measured != plan.Samples || summary.Planned != plan.Samples || len(summary.Samples) != plan.Samples+plan.Warmups {
		return plan, first, nil, errors.New("comparison requires complete baseline/worker interruption populations with at least 30 measured samples")
	}
	for _, role := range []string{"binary", "config", "fixture", "interrupt_fixture"} {
		if plan.Roles[role] == "" {
			return plan, first, nil, fmt.Errorf("missing pinned input role %s", role)
		}
	}
	if len(plan.Runtime) == 0 {
		return plan, first, nil, errors.New("comparison requires declared native/model runtime asset hashes")
	}
	if err := verifyControlInputs(plan.Inputs); err != nil {
		return plan, first, nil, err
	}
	if plan.Inputs[plan.ScenarioPath] == "" {
		return plan, first, nil, errors.New("missing pinned control scenario")
	}
	scn, err := loadScenario(plan.ScenarioPath)
	if err != nil {
		return plan, first, nil, err
	}
	control, actualCondition, err := populationControl(scn)
	if err != nil || actualCondition != condition || controlSpecHash(control) != plan.ControlSpec || controlScenarioHash(scn, control) != plan.ScenarioSpec {
		return plan, first, nil, errors.New("control scenario does not match population condition")
	}
	values := make([]time.Duration, 0, plan.Samples)
	for i, sample := range summary.Samples {
		if sample.Index != i+1 || sample.Warmup != (i < plan.Warmups) || sample.Error != "" || sample.Timings["detection_to_stopped"] <= 0 || sample.Workload == nil || len(sample.Artifacts) == 0 {
			return plan, first, nil, fmt.Errorf("invalid control observation %d", i+1)
		}
		if err := validateInterruptionWorkload(*sample.Workload, condition); err != nil {
			return plan, first, nil, err
		}
		if err := verifyControlInputs(sample.Artifacts); err != nil {
			return plan, first, nil, err
		}
		var actual controlPopulationSample
		if sample.Run == "" {
			return plan, first, nil, errors.New("missing original child run")
		}
		if err := readControlSample(filepath.Dir(sample.Run), scn, &actual); err != nil {
			return plan, first, nil, err
		}
		if !maps.Equal(actual.Timings, sample.Timings) || !reflect.DeepEqual(actual.Workload, sample.Workload) || !maps.Equal(actual.Artifacts, sample.Artifacts) {
			return plan, first, nil, errors.New("population sample differs from original child evidence")
		}
		if err := compareControlIdentity(actual, sample); err != nil {
			return plan, first, nil, err
		}
		if i == 0 {
			first = sample
		} else if err := compareControlIdentity(first, sample); err != nil {
			return plan, first, nil, err
		}
		if !sample.Warmup {
			values = append(values, sample.Timings["detection_to_stopped"])
		}
	}
	return plan, first, values, nil
}

func controlSpecHash(t Turn) string {
	o := t.interruptionStimulusOptions()
	data, _ := json.Marshal(struct {
		Operation, Expected            string
		Timeout, Delay, CompleteWithin time.Duration
	}{t.Op, t.ExpectedTranscript, t.Timeout.Duration, o.delay, o.completeWithin})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func controlScenarioHash(scn *Scenario, t Turn) string {
	normalized := *scn
	normalized.Name, normalized.Description, normalized.LatencyCondition = "", "", ""
	normalized.Worker.SustainedLoad = false
	normalized.HistoryFixture = ""                  // content is pinned as a separate input role
	t.ID, t.WAV, t.InterruptWAV = "control", "", "" // content hashes are separate roles
	normalized.Turns = []Turn{t}
	data, _ := json.Marshal(normalized)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func summarizeControlTimes(values []time.Duration) controlTimingSummary {
	v := append([]time.Duration(nil), values...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return controlTimingSummary{Samples: len(v), P50: v[(len(v)*50+99)/100-1], P95: v[(len(v)*95+99)/100-1]}
}
