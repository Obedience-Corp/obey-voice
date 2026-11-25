package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestJobControlScenariosLoad(t *testing.T) {
	for name, want := range map[string]bool{"job-control-cancel": true, "job-control-off": false, "job-control-report": true, "job-control-steer": true} {
		scn, err := loadScenario(filepath.Join("scenarios", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if scn.Brain.JobControl == nil || *scn.Brain.JobControl != want || scn.RouterTools != "legacy" {
			t.Fatalf("%s: job_control %v, router_tools %q", name, scn.Brain.JobControl, scn.RouterTools)
		}
	}
	if _, err := loadScenarioString("t", "name: t\nrouter_tools: route_only\nbrain: { job_control: true }\n"); err == nil ||
		!strings.Contains(err.Error(), "router_tools: legacy") {
		t.Fatalf("job control off the direct router loaded: %v", err)
	}
}

// The scenario decides the run's job control, whatever the operator's
// configuration says.
func TestResolveConfigTakesJobControlFromTheScenario(t *testing.T) {
	for _, want := range []bool{true, false} {
		body := "name: t\nrouter_tools: legacy\ntts: { provider: none }\n"
		if want {
			body += "brain: { job_control: true }\n"
		}
		scn, err := loadScenarioString("t", body)
		if err != nil {
			t.Fatal(err)
		}
		base := &config.Config{BrainProvider: "ollama", OllamaModel: "m", VoiceJobControl: !want}
		cfg, prof, err := resolveConfig(base, scn, knobs{}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.VoiceJobControl != want || prof.JobControl != want || cfg.VoiceRouterTools != "legacy" {
			t.Fatalf("want %t: config %t, profile %t, router %q", want, cfg.VoiceJobControl, prof.JobControl, cfg.VoiceRouterTools)
		}
	}
}

func TestJobControlExpectations(t *testing.T) {
	at := time.Now()
	applied := []timedEvent{
		{at, events.RouteDecided{Kind: "job.control", State: "handled"}},
		{at, events.RouteDecided{Kind: "job.control", State: "applied", JobID: "job-1", Message: "stopped"}},
		{at, events.JobAborted{ID: "job-1", Reason: events.AbortCancelled}},
	}
	st := turnState{events: applied, spoken: "Stopped after 7 seconds."}
	for _, ex := range []Expectation{
		{JobControl: "applied"}, {JobControl: "applied:stopped"}, {AbortReason: "cancelled"}, {SpokenOnce: `(?i)\bstopped\b|\bcancelled\b`},
	} {
		if err := evalExpect(ex, &recorder{}, st); err != nil {
			t.Errorf("%+v: %v", ex, err)
		}
	}
	for _, ex := range []Expectation{
		{JobControl: "refused"}, {JobControl: "applied:stopping"}, {JobControl: "none"}, {AbortReason: "budget"},
	} {
		if err := evalExpect(ex, &recorder{}, st); err == nil {
			t.Errorf("%+v passed", ex)
		}
	}
	twice := turnState{spoken: "Cancelled it. Stopped after 7 seconds."}
	if err := evalExpect(Expectation{SpokenOnce: `(?i)\bstopped\b|\bcancelled\b`}, &recorder{}, twice); err == nil {
		t.Error("two matches passed spoken_once")
	}
	none := turnState{events: []timedEvent{{at, events.RouteDecided{Kind: "spawn", State: "accepted"}}}}
	if err := evalExpect(Expectation{JobControl: "none"}, &recorder{}, none); err != nil {
		t.Error(err)
	}
	refused := turnState{events: []timedEvent{{at, events.RouteDecided{Kind: "job.control", State: "refused", Message: "bare_stop"}}}}
	if err := evalExpect(Expectation{JobControl: "refused:bare_stop"}, &recorder{}, refused); err != nil {
		t.Error(err)
	}
}

// The live brain the harness wraps still reports its own job control setting,
// which is what the host's grant asks.
func TestObservedOllamaKeepsItsJobControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"m"}]}`))
	}))
	defer server.Close()
	config.SetConfigDirForTest(t, t.TempDir())
	for _, on := range []bool{true, false} {
		inner, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "m", PromptsDir: t.TempDir(), VoiceJobControl: on})
		if err != nil {
			t.Fatal(err)
		}
		wrapped, _ := observeProvider(inner)
		if got := brain.DecodesJobControl(wrapped); got != on {
			t.Fatalf("job control %t, wrapped provider reports %t", on, got)
		}
		_ = inner.Close()
	}
}

// The direct router's log of a persona's report: the live turn's own tool
// calls carry no job id, the worker's last call started before the report,
// and a correction that was waiting is settled after it.
func directReportLog(at time.Time, lateWorkerTool bool) []timedEvent {
	log := []timedEvent{
		{at, events.ToolCallStarted{ID: "w1", JobID: "job-1", Name: "write_file"}},
		{at, events.RouteDecided{Kind: "job.control", State: "handled"}},
		{at, events.ToolCallStarted{ID: "live-1", Name: "list_files"}},
		{at, events.JobReport{ID: "job-1", Checkpoint: "wrote 1 file (1 succeeded)", Answer: true}},
		{at, events.JobCorrection{ID: "job-1", State: "not_applied"}},
		{at, events.RouteDecided{Kind: "job.control", State: "applied", JobID: "job-1", Message: "accepted", Action: "report"}},
		{at, events.ToolCallStarted{ID: "live-2", Name: "read_file"}},
	}
	if lateWorkerTool {
		log = append(log, timedEvent{at, events.ToolCallStarted{ID: "w2", JobID: "job-1", Name: "write_file"}})
	}
	return append(log, timedEvent{at, events.JobFinished{ID: "job-1", Summary: "Wrote one file.", CutShort: true, CutShortReason: "report requested"}})
}

func TestJobControlReportExpectationsOnTheDirectRouter(t *testing.T) {
	at := time.Now()
	st := turnState{events: directReportLog(at, false), spoken: "I'll work on that and report back. I asked it to report, and it's writing up what it has; so far: wrote 1 file."}
	for _, ex := range []Expectation{
		{JobControl: "applied:accepted"}, {JobControlAction: "report"}, {Event: "job_report"}, {ToolsStopAfterReport: boolPtr(true)},
		{CutShort: "report requested"}, {NoEvent: "job_started"}, {SpokenOnce: "(?i)writing up what it has"},
		{ReportCheckpointMatches: "succeeded"}, {ReportCheckpointNotMatches: "(?i)unknown"},
	} {
		if err := evalExpect(ex, &recorder{}, st); err != nil {
			t.Errorf("%+v: %v", ex, err)
		}
	}
	for _, ex := range []Expectation{{JobControlAction: "cancel"}, {JobControlAction: "steer"}} {
		if err := evalExpect(ex, &recorder{}, st); err == nil {
			t.Errorf("%+v passed", ex)
		}
	}
	late := turnState{events: directReportLog(at, true)}
	if err := evalExpect(Expectation{ToolsStopAfterReport: boolPtr(true)}, &recorder{}, late); err == nil {
		t.Error("a worker tool started after the report passed tools_stop_after_report")
	}
}

func TestReportCheckpointExpectations(t *testing.T) {
	at := time.Now()
	settled := timedEvent{at, events.RouteDecided{Kind: "job.control", State: "applied", JobID: "job-1", Message: "accepted", Action: "report"}}
	report := func(id, checkpoint string) timedEvent {
		return timedEvent{at, events.JobReport{ID: id, Checkpoint: checkpoint, Answer: true}}
	}
	notUnknown := Expectation{ReportCheckpointNotMatches: "(?i)unknown"}
	succeeded := Expectation{ReportCheckpointMatches: "succeeded"}
	for name, tc := range map[string]struct {
		log  []timedEvent
		ex   Expectation
		pass bool
	}{
		"succeeded":                 {[]timedEvent{report("job-1", "wrote 2 files (2 succeeded)"), settled}, notUnknown, true},
		"one unknown":               {[]timedEvent{report("job-1", "wrote 2 files (1 succeeded, 1 unknown)"), settled}, notUnknown, false},
		"no report":                 {[]timedEvent{settled}, notUnknown, false},
		"another job's report":      {[]timedEvent{report("job-2", "wrote 1 file (1 succeeded)"), settled}, notUnknown, false},
		"no activity yet":           {[]timedEvent{report("job-1", "no tool activity recorded yet"), settled}, succeeded, false},
		"activity":                  {[]timedEvent{report("job-1", "listed 1 folder (1 succeeded)"), settled}, succeeded, true},
		"no settlement, any report": {[]timedEvent{report("job-3", "read 1 file (1 succeeded)")}, succeeded, true},
	} {
		err := evalExpect(tc.ex, &recorder{}, turnState{events: tc.log})
		if (err == nil) != tc.pass {
			t.Errorf("%s: %+v: %v", name, tc.ex, err)
		}
	}
}

func TestJobControlActionReadsTheSettlement(t *testing.T) {
	at := time.Now()
	settled := func(state, action string) timedEvent {
		return timedEvent{at, events.RouteDecided{Kind: "job.control", State: state, JobID: "job-1", Message: "accepted", Action: action}}
	}
	for _, action := range []string{"cancel", "report", "steer"} {
		log := []timedEvent{{at, events.RouteDecided{Kind: "job.control", State: "handled"}}, settled("applied", action)}
		for _, want := range []string{"cancel", "report", "steer"} {
			err := evalExpect(Expectation{JobControlAction: want}, &recorder{}, turnState{events: log})
			if (err == nil) != (want == action) {
				t.Errorf("settled %s, job_control_action %s: %v", action, want, err)
			}
		}
	}
	// A report that settles a waiting correction is still a report.
	withCorrection := []timedEvent{{at, events.JobReport{ID: "job-1"}}, {at, events.JobCorrection{ID: "job-1", State: "not_applied"}}, settled("applied", "report")}
	if err := evalExpect(Expectation{JobControlAction: "report"}, &recorder{}, turnState{events: withCorrection}); err != nil {
		t.Errorf("report with a waiting correction: %v", err)
	}
	for name, log := range map[string][]timedEvent{
		"refused report": {{at, events.JobReport{ID: "job-1"}}, settled("refused", "report")},
		"no settlement":  {{at, events.JobReport{ID: "job-1"}}},
		"two settled":    {settled("applied", "report"), settled("applied", "report")},
	} {
		if err := evalExpect(Expectation{JobControlAction: "report"}, &recorder{}, turnState{events: log}); err == nil {
			t.Errorf("%s passed job_control_action report", name)
		}
	}
	if _, err := loadScenarioString("t", "name: t\nturns:\n  - { id: a, user: hi, expect: [ { job_control_action: pause } ] }\n"); err == nil ||
		!strings.Contains(err.Error(), "job_control_action") {
		t.Fatalf("an unknown action loaded: %v", err)
	}
}

func TestWorkFileAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "last.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := evalExpect(Expectation{WorkFileAbsent: "omega.txt"}, &recorder{}, turnState{workDir: dir}); err != nil {
		t.Errorf("absent file: %v", err)
	}
	if err := evalExpect(Expectation{WorkFileAbsent: "last.txt"}, &recorder{}, turnState{workDir: dir}); err == nil {
		t.Error("a file at depth passed work_file_absent")
	}
	if err := evalExpect(Expectation{WorkFileAbsent: "last.txt"}, &recorder{}, turnState{}); err == nil {
		t.Error("no work directory passed work_file_absent")
	}
}

// A job-control scenario is skipped, not failed, where the operator's settings
// cannot give it the direct router and the legacy job events.
func TestJobControlSkipReason(t *testing.T) {
	tiered := config.DefaultVoiceWorkers()
	tiered.Mode = config.WorkerModeTiered
	for name, tc := range map[string]struct {
		cfg  config.Config
		skip bool
	}{
		"direct":     {config.Config{VoiceRouterTools: "legacy"}, false},
		"work inbox": {config.Config{VoiceRouterTools: "legacy", VoiceWorkEnabled: true}, true},
		"tiered":     {config.Config{VoiceRouterTools: "legacy", VoiceWorkers: tiered}, true},
		"route only": {config.Config{VoiceRouterTools: "route_only"}, true},
	} {
		if got := jobControlSkipReason(&tc.cfg) != ""; got != tc.skip {
			t.Errorf("%s: skip %t, want %t", name, got, tc.skip)
		}
	}
	for _, body := range []string{"brain: { job_control: true }\n", "brain: { job_control: false }\n"} {
		scn, err := loadScenarioString("t", "name: t\nrouter_tools: legacy\ntts: { provider: none }\n"+body)
		if err != nil {
			t.Fatal(err)
		}
		base := &config.Config{BrainProvider: "ollama", OllamaModel: "m", VoiceWorkEnabled: true}
		var skip skipError
		if _, _, err := resolveConfig(base, scn, knobs{}, t.TempDir()); !errors.As(err, &skip) {
			t.Fatalf("%q with the work inbox on: %v, want a skip", body, err)
		}
	}
	plain, err := loadScenarioString("t", "name: t\nrouter_tools: legacy\ntts: { provider: none }\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveConfig(&config.Config{BrainProvider: "ollama", OllamaModel: "m", VoiceWorkEnabled: true}, plain, knobs{}, t.TempDir()); err != nil {
		t.Fatalf("a scenario without job control was refused: %v", err)
	}
}
