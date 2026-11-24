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
	for name, want := range map[string]bool{"job-control-cancel": true, "job-control-off": false} {
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
