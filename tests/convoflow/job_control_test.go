package main

import (
	"net/http"
	"net/http/httptest"
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
		if scn.Brain.JobControl != want || scn.RouterTools != "legacy" {
			t.Fatalf("%s: job_control %t, router_tools %q", name, scn.Brain.JobControl, scn.RouterTools)
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
// calls carry no job id, and the worker's last call started before the report.
func directReportLog(at time.Time, lateWorkerTool bool) []timedEvent {
	log := []timedEvent{
		{at, events.ToolCallStarted{ID: "w1", JobID: "job-1", Name: "write_file"}},
		{at, events.RouteDecided{Kind: "job.control", State: "handled"}},
		{at, events.ToolCallStarted{ID: "live-1", Name: "list_files"}},
		{at, events.JobReport{ID: "job-1", Checkpoint: "wrote 1 file (1 succeeded)", Answer: true}},
		{at, events.RouteDecided{Kind: "job.control", State: "applied", JobID: "job-1", Message: "accepted"}},
		{at, events.SpeechSegmentReady{Text: "I asked it to report, and it's writing up what it has; so far: wrote 1 file."}},
		{at, events.GeneratingVoice{Sentence: "I asked it to report, and it's writing up what it has; so far: wrote 1 file."}},
		{at, events.ToolCallStarted{ID: "live-2", Name: "read_file"}},
	}
	if lateWorkerTool {
		log = append(log, timedEvent{at, events.ToolCallStarted{ID: "w2", JobID: "job-1", Name: "write_file"}})
	}
	return append(log, timedEvent{at, events.JobFinished{ID: "job-1", Summary: "Wrote one file.", CutShort: true, CutShortReason: "report requested"}})
}

func TestJobControlReportExpectationsOnTheDirectRouter(t *testing.T) {
	at := time.Now()
	st := turnState{events: directReportLog(at, false), spoken: "I'll work on that and report back. So far: read 1 file (1 unknown). I asked it to report, and it's writing up what it has; so far: wrote 1 file."}
	for _, ex := range []Expectation{
		{JobControl: "applied:accepted"}, {JobControlAction: "report"}, {Event: "job_report"}, {ToolsStopAfterReport: boolPtr(true)},
		{CutShort: "report requested"}, {NoEvent: "job_started"}, {SpokenOnce: "(?i)writing up what it has"},
		{TurnSpokenNotMatches: `(?i)\bunknown\b`},
	} {
		if err := evalExpect(ex, &recorder{}, st); err != nil {
			t.Errorf("%+v: %v", ex, err)
		}
	}
	for _, ex := range []Expectation{
		{JobControlAction: "cancel"}, {JobControlAction: "steer"}, {SpokenNotMatches: `(?i)\bunknown\b`},
	} {
		if err := evalExpect(ex, &recorder{}, st); err == nil {
			t.Errorf("%+v passed", ex)
		}
	}
	late := turnState{events: directReportLog(at, true)}
	if err := evalExpect(Expectation{ToolsStopAfterReport: boolPtr(true)}, &recorder{}, late); err == nil {
		t.Error("a worker tool started after the report passed tools_stop_after_report")
	}
	said := turnState{events: append(directReportLog(at, false), timedEvent{at, events.SpeechSegmentReady{Text: "Read 1 file (1 unknown)."}})}
	if err := evalExpect(Expectation{TurnSpokenNotMatches: `(?i)\bunknown\b`}, &recorder{}, said); err == nil {
		t.Error("the turn spoke unknown and turn_spoken_not_matches passed")
	}
}

func TestJobControlActionTellsTheActions(t *testing.T) {
	at := time.Now()
	settled := func(id string) timedEvent {
		return timedEvent{at, events.RouteDecided{Kind: "job.control", State: "applied", JobID: id, Message: "accepted"}}
	}
	for _, tc := range []struct {
		name string
		log  []timedEvent
		want string
	}{
		{"cancel", []timedEvent{settled("job-1"), {at, events.JobAborted{ID: "job-1", Reason: events.AbortCancelled}}}, "cancel"},
		{"report", []timedEvent{{at, events.JobReport{ID: "job-1"}}, settled("job-1")}, "report"},
		{"steer", []timedEvent{settled("job-1"), {at, events.JobCorrection{ID: "job-1", State: "pending"}}, {at, events.JobCorrection{ID: "job-1", State: "included"}}}, "steer"},
	} {
		for _, action := range []string{"cancel", "report", "steer"} {
			err := evalExpect(Expectation{JobControlAction: action}, &recorder{}, turnState{events: tc.log})
			if (err == nil) != (action == tc.want) {
				t.Errorf("%s log, job_control_action %s: %v", tc.name, action, err)
			}
		}
	}
	for name, log := range map[string][]timedEvent{
		"another job":      {settled("job-1"), {at, events.JobReport{ID: "job-2"}}},
		"refused":          {{at, events.RouteDecided{Kind: "job.control", State: "refused", Message: "unlisted"}}, {at, events.JobReport{ID: "job-1"}}},
		"no settlement":    {{at, events.JobReport{ID: "job-1"}}},
		"two actions seen": {settled("job-1"), {at, events.JobReport{ID: "job-1"}}, {at, events.JobCorrection{ID: "job-1"}}},
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
