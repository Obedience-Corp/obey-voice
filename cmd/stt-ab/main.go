// Command stt-ab turns a raw `obey-voice benchmark` run into the curated
// STT A/B decision record that docs/benchmarks/ stores.
//
// No code emits that schema today: L2 and L4 were aggregated by hand, so a
// gate written against their fields could not be evaluated from a harness run.
// This closes that gap. It computes only what arithmetic can settle; the
// criteria, the verdicts, and the prose stay human-written, because "criterion
// met" is a judgement against criteria fixed before the numbers existed.
//
// Usage:
//
//	obey-voice benchmark --stt-provider sherpa-parakeet --stt-provider transcribecpp \
//	  --iterations 3 --audio-fixture ... --expect-text ... --json-file raw.json
//
//	stt-ab -in raw.json -control sherpa-parakeet -candidate transcribecpp \
//	  -item T1 -date 2026-08-26 -out docs/benchmarks/T1-stt-ab-transcribecpp-2026-08-26.json
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
		fmt.Fprintln(os.Stderr, "stt-ab:", err)
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
	flag.StringVar(&o.control, "control", "", "provider alias to treat as the control (required)")
	flag.StringVar(&o.candidate, "candidate", "", "provider alias under test (required)")
	flag.StringVar(&o.item, "item", "", "artifact label, e.g. T1")
	flag.StringVar(&o.date, "date", "", "run date, YYYY-MM-DD")
	flag.StringVar(&o.question, "question", "", "the question this run answers")
	flag.BoolVar(&o.dropWarmup, "drop-warmup", false, "exclude iteration 1, as scripts/bench.sh does for latency; the committed stt-ab records did not")
	flag.StringVar(&o.prov.ObeyVoiceCommit, "commit", "", "obey-voice commit the run was built from")
	flag.StringVar(&o.prov.Machine, "machine", "", "machine identifier, e.g. Mac16,9")
	flag.StringVar(&o.prov.CPU, "cpu", "", "cpu, e.g. Apple M4 Max")
	flag.StringVar(&o.prov.OS, "os", "", "os version")
	flag.StringVar(&o.prov.Backend, "backend", "", "compute backend that actually ran, e.g. metal, cuda, cpu")
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
	}
	byProvider := STTRunsByProvider(results)
	if len(byProvider) == 0 {
		return fmt.Errorf("%s has no mode=\"stt\" runs — was the benchmark run without --stt-provider?", o.in)
	}
	controlRuns, err := runsFor(byProvider, o.control)
	if err != nil {
		return err
	}
	candidateRuns, err := runsFor(byProvider, o.candidate)
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
	fmt.Fprintf(os.Stderr, "stt-ab: wrote %s\n", o.out)
	return nil
}

func runsFor(byProvider map[string][]rawResult, name string) ([]rawResult, error) {
	runs, ok := byProvider[name]
	if !ok {
		return nil, fmt.Errorf("no runs for provider %q; the file has: %s", name, strings.Join(providerNames(byProvider), ", "))
	}
	return runs, nil
}

func providerNames(byProvider map[string][]rawResult) []string {
	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func buildReport(o options, control, candidate ProviderStats) Report {
	return Report{
		Schema:   Schema,
		Item:     o.item,
		Date:     o.date,
		Question: o.question,
		CriteriaFixedInAdvance: map[string]string{
			"accuracy":      "",
			"latency":       "",
			"reliability":   "",
			"decision_rule": "",
		},
		Control:   o.control,
		Candidate: o.candidate,
		Providers: map[string]ProviderStats{
			o.control:   control,
			o.candidate: candidate,
		},
		Result:            Compare(control, candidate),
		PercentileMethod:  percentileMethod,
		WarmupDropped:     o.dropWarmup,
		Provenance:        withIterations(o.prov, control, candidate),
		ReadingTheNumbers: "",
		EvidenceLimits:    "",
		DurableValue:      "",
	}
}

func withIterations(prov Provenance, control, candidate ProviderStats) Provenance {
	prov.Iterations = control.Iterations
	if candidate.Iterations > prov.Iterations {
		prov.Iterations = candidate.Iterations
	}
	prov.Aggregator = "cmd/stt-ab"
	return prov
}

// warnUnfilled names every field a human still owes the artifact. Silence here
// is how an artifact ships with no provenance and no criteria, which is the
// thing docs/benchmarks/README.md exists to prevent.
func warnUnfilled(o options, report Report) {
	var missing []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"-item", o.item},
		{"-date", o.date},
		{"-question", o.question},
		{"-commit", report.Provenance.ObeyVoiceCommit},
		{"-machine", report.Provenance.Machine},
		{"-cpu", report.Provenance.CPU},
		{"-os", report.Provenance.OS},
		{"-backend", report.Provenance.Backend},
		{"-fixtures", report.Provenance.Fixtures},
		{"-command", report.Provenance.Command},
	} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "stt-ab: provenance incomplete, fill before committing: %s\n", strings.Join(missing, " "))
	}
	fmt.Fprintln(os.Stderr, "stt-ab: criteria_fixed_in_advance, reading_the_numbers and evidence_limits are left blank on purpose — write them yourself.")
}
