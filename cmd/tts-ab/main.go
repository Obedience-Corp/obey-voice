// Command tts-ab turns a raw `obey-voice benchmark` run into the curated TTS
// A/B decision record that docs/benchmarks/ stores.
//
// It is the TTS counterpart of cmd/stt-ab, and exists for the same reason:
// N7's bake-off and F7's RTF measurements were assembled by hand, so a gate
// written against their fields could not be evaluated from a harness run.
//
// It computes only what arithmetic can settle. The criteria, the verdicts and
// the prose stay human-written, because "criterion met" is a judgement against
// criteria fixed before the numbers existed.
//
// Usage:
//
//	obey-voice benchmark --tts-provider kokoro --tts-provider qwen3-tts:0.6b \
//	  --iterations 3 --full-turn-fixture ... --json-file raw.json
//
//	tts-ab -in raw.json -control kokoro -candidate qwen3-tts:0.6b \
//	  -item V1 -date 2026-08-26 -out docs/benchmarks/V1-tts-ab-qwen-2026-08-26.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tts-ab:", err)
		os.Exit(1)
	}
}

type options struct {
	in         string
	out        string
	control    string
	candidate  string
	item       string
	date       string
	question   string
	dropWarmup bool
	prov       Provenance
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.in, "in", "", "raw `obey-voice benchmark --json-file` output (required)")
	flag.StringVar(&o.out, "out", "", "artifact path; stdout when empty")
	flag.StringVar(&o.control, "control", "", "engine to treat as the control, e.g. kokoro (required)")
	flag.StringVar(&o.candidate, "candidate", "", "engine under test, e.g. qwen3-tts:1.7b (required)")
	flag.StringVar(&o.item, "item", "", "artifact label, e.g. V1")
	flag.StringVar(&o.date, "date", "", "run date, YYYY-MM-DD")
	flag.StringVar(&o.question, "question", "", "the question this run answers")
	flag.BoolVar(&o.dropWarmup, "drop-warmup", true, "exclude iteration 1; a cold Qwen worker pays 7-11s of model load on its first synthesis")
	flag.StringVar(&o.prov.ObeyVoiceCommit, "commit", "", "obey-voice commit the run was built from")
	flag.StringVar(&o.prov.Machine, "machine", "", "machine identifier, e.g. Mac16,9")
	flag.StringVar(&o.prov.CPU, "cpu", "", "cpu, e.g. Apple M4 Max")
	flag.StringVar(&o.prov.OS, "os", "", "os version")
	flag.StringVar(&o.prov.Backend, "backend", "", "compute backend that actually ran, e.g. metal, cuda, cpu")
	flag.StringVar(&o.prov.BrainProvider, "brain-provider", "", "brain provider the turns ran against")
	flag.StringVar(&o.prov.BrainModel, "brain-model", "", "brain model — turn timings are meaningless without it")
	flag.StringVar(&o.prov.Persona, "persona", "", "active persona")
	flag.StringVar(&o.prov.Fixtures, "fixtures", "", "fixture kind and count — say whether they are synthetic or recorded")
	flag.StringVar(&o.prov.Command, "command", "", "the benchmark command that produced -in")
	flag.Parse()
	return o
}

func run() error {
	o := parseFlags()
	if o.in == "" || o.control == "" || o.candidate == "" {
		flag.Usage()
		return fmt.Errorf("-in, -control and -candidate are required")
	}
	if o.control == o.candidate {
		return fmt.Errorf("-control and -candidate are both %q", o.control)
	}

	data, err := os.ReadFile(o.in)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.in, err)
	}
	results, err := ParseResults(data)
	if err != nil {
		return err
	}

	if o.dropWarmup {
		results = DropWarmup(results)
		if len(results) == 0 {
			return fmt.Errorf("%s has no runs left after -drop-warmup — it holds only iteration 1", o.in)
		}
	}
	byEngine := TurnsByEngine(results)
	if len(byEngine) == 0 {
		return fmt.Errorf("%s has no turns that reported a TTS engine — was it run with --no-voice, or built before TurnMetrics carried TTSProvider?", o.in)
	}
	controlRuns, err := runsFor(byEngine, o.control)
	if err != nil {
		return err
	}
	candidateRuns, err := runsFor(byEngine, o.candidate)
	if err != nil {
		return err
	}

	report := buildReport(o, StatsFor(controlRuns), StatsFor(candidateRuns))
	warnUnfilled(o, report)

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifact: %w", err)
	}
	encoded = append(encoded, '\n')
	if o.out == "" {
		_, err = os.Stdout.Write(encoded)
		return err
	}
	if err := os.WriteFile(o.out, encoded, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", o.out, err)
	}
	fmt.Fprintf(os.Stderr, "tts-ab: wrote %s\n", o.out)
	return nil
}

// runsFor resolves one engine, naming what was actually present when it is
// missing. The engine key is what the pipeline reported, which is not
// necessarily what --tts-provider asked for, so the error has to say so.
func runsFor(byEngine map[string][]rawResult, name string) ([]rawResult, error) {
	if runs, ok := byEngine[name]; ok {
		return runs, nil
	}
	present := make([]string, 0, len(byEngine))
	for k := range byEngine {
		present = append(present, k)
	}
	sort.Strings(present)
	return nil, fmt.Errorf("no turns synthesized by %q; the run reported: %s", name, strings.Join(present, ", "))
}

func buildReport(o options, control, candidate EngineStats) Report {
	iterations := control.Iterations
	if candidate.Iterations > iterations {
		iterations = candidate.Iterations
	}
	prov := o.prov
	prov.Iterations = iterations
	prov.Aggregator = "cmd/tts-ab"

	return Report{
		Schema:                 Schema,
		Item:                   o.item,
		Date:                   o.date,
		Question:               o.question,
		CriteriaFixedInAdvance: nil,
		Control:                o.control,
		Candidate:              o.candidate,
		Engines: map[string]EngineStats{
			o.control:   control,
			o.candidate: candidate,
		},
		Result:           Compare(control, candidate),
		PercentileMethod: percentileMethod,
		WarmupDropped:    o.dropWarmup,
		Provenance:       prov,
	}
}

// warnUnfilled prints, on stderr, every field arithmetic cannot settle. An
// artifact committed with these blank is a measurement without a decision, and
// the point of the record is the decision.
func warnUnfilled(o options, report Report) {
	var missing []string
	if len(report.CriteriaFixedInAdvance) == 0 {
		missing = append(missing, "criteria_fixed_in_advance")
	}
	if report.Question == "" {
		missing = append(missing, "question")
	}
	if report.Result.LatencyCriterionMet == nil {
		missing = append(missing, "result.latency_criterion_met")
	}
	if report.Result.NaturalnessCriterionMet == nil {
		missing = append(missing, "result.naturalness_criterion_met")
	}
	if report.Result.ReliabilityCriterionMet == nil {
		missing = append(missing, "result.reliability_criterion_met")
	}
	if report.Result.Decision == "" {
		missing = append(missing, "result.decision")
	}
	if report.ReadingTheNumbers == "" {
		missing = append(missing, "reading_the_numbers")
	}
	if report.EvidenceLimits == "" {
		missing = append(missing, "evidence_limits")
	}
	if report.DurableValue == "" {
		missing = append(missing, "durable_value")
	}
	if report.Provenance.Machine == "" {
		missing = append(missing, "provenance.machine")
	}
	if report.Provenance.BrainModel == "" {
		missing = append(missing, "provenance.brain_model")
	}
	if report.Provenance.Fixtures == "" {
		missing = append(missing, "provenance.fixtures")
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "tts-ab: still yours to write: %s\n", strings.Join(missing, ", "))
	}

	// A substitution is the one failure that reads as a clean result: the
	// column is labelled with the engine that ran, so a leg asked for one
	// engine and served by another looks like a successful measurement of the
	// wrong thing.
	for name, stats := range report.Engines {
		for _, requested := range stats.RequestedAs {
			if requested != "" && requested != name {
				fmt.Fprintf(os.Stderr, "tts-ab: warning: %d turn(s) labelled %q were requested as %q\n", stats.Turns, name, requested)
			}
		}
		if stats.TurnsWithFallback > 0 {
			fmt.Fprintf(os.Stderr, "tts-ab: warning: %q had %d turn(s) with a fallback segment; those are not clean measurements of it\n", name, stats.TurnsWithFallback)
		}
		if stats.TurnsWithoutAudio > 0 {
			fmt.Fprintf(os.Stderr, "tts-ab: warning: %q produced no audio on %d of %d turns\n", name, stats.TurnsWithoutAudio, stats.Turns)
		}
		if stats.TurnsStallWatchdogFired > 0 {
			fmt.Fprintf(os.Stderr, "tts-ab: warning: %q tripped the playback-stall watchdog on %d turn(s)\n", name, stats.TurnsStallWatchdogFired)
		}
	}
}
