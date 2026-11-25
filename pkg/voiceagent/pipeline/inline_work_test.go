package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func workPersona(work string, tools bool) *config.Config {
	return &config.Config{ActivePersona: "lumen", AgentName: "Lumen", BrainProvider: "ollama", OllamaModel: "persona-model",
		PersonaWork: work, VoiceToolsEnabled: tools, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker-model"}
}

type workPipeline struct {
	p      *Pipeline
	b      *optionsRecordingBrain
	jobs   *job.Table
	routes chan events.RouteDecided
}

func newWorkPipeline(t *testing.T, cfg *config.Config, reply string) *workPipeline {
	t.Helper()
	w := &workPipeline{b: &optionsRecordingBrain{fakeBrain: &fakeBrain{chunks: []string{reply}}}, routes: make(chan events.RouteDecided, 8)}
	w.p = &Pipeline{Brain: w.b, Events: events.NewBus(), VoiceToolsEnabled: cfg.VoiceToolsEnabled, VoiceFailedRouteRepair: true}
	w.p.SetOutputMuted(true)
	events.Subscribe(w.p.Events, func(e events.RouteDecided) { w.routes <- e })
	w.jobs = job.NewTable(t.Context())
	t.Cleanup(w.jobs.Close)
	NewAgency(w.p, w.jobs, &recordingWorker{reply: "done"}, cfg, nil).Wire()
	return w
}

func (w *workPipeline) spawned() bool {
	for {
		select {
		case r := <-w.routes:
			if r.Kind == string(RouteSpawn) {
				return true
			}
		default:
			return len(w.jobs.Status()) > 0
		}
	}
}

// The phrase shortcuts that hand a request to a job before the model runs
// leave an inline persona's turn to the persona.
func TestInlinePersonaStartsNoJobFromPhraseShortcuts(t *testing.T) {
	for _, tc := range []struct {
		input string
		tools bool
	}{
		{"Show me a picture of a red fox.", true},
		{"Search the web for the tide tables.", true},
		{"Build this project.", true},
		{"Search for the release notes.", false},
		{"Search the web for the tide tables.", false},
	} {
		for _, work := range []string{config.WorkInline, config.WorkWorker} {
			w := newWorkPipeline(t, workPersona(work, tc.tools), "Here you go.")
			kind, _, _ := w.p.classifyUserTurn(tc.input)
			if (kind == RouteSpawn) == (work == config.WorkInline) {
				t.Errorf("%s, tools %v: %q routed %q", work, tc.tools, tc.input, kind)
			}
		}
	}

	w := newWorkPipeline(t, workPersona(config.WorkInline, true), "Here is a red fox.")
	if err := w.p.RunTurnTextMode(t.Context(), "Show me a picture of a red fox."); err != nil {
		t.Fatal(err)
	}
	if w.spawned() || len(w.b.opts) != 1 {
		t.Fatalf("inline picture request: spawned %v, model ran %d time(s)", w.spawned(), len(w.b.opts))
	}
	if opts := w.b.opts[0]; opts.BackgroundWorker || opts.RouterToolsPolicy != brain.RouterToolsDirect || !opts.ToolsEnabled {
		t.Fatalf("inline turn options: background worker %v, policy %q, tools %v", opts.BackgroundWorker, opts.RouterToolsPolicy, opts.ToolsEnabled)
	}
}

// The model of an inline persona is never offered a hand-off, so a web
// search or a second tool round stays on its turn, and route_only or tiered
// settings do not take its tools away for a route it could not use.
func TestInlinePersonaKeepsItsToolsOnEveryRouterSetting(t *testing.T) {
	tiered := func(work string) *config.Config {
		cfg := tierTestConfig()
		cfg.PersonaWork, cfg.VoiceToolsEnabled = work, true
		return cfg
	}
	for _, tc := range []struct {
		name   string
		cfg    *config.Config
		router brain.RouterToolsPolicy
		want   brain.RouterToolsPolicy
	}{
		{"direct inline", workPersona(config.WorkInline, true), "", brain.RouterToolsDirect},
		{"route_only inline", workPersona(config.WorkInline, true), brain.RouterToolsRouteOnly, brain.RouterToolsDirect},
		{"tiered inline", tiered(config.WorkInline), "", brain.RouterToolsDirect},
		{"tiered worker", tiered(""), "", brain.RouterToolsRouteOnly},
	} {
		for _, voice := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/text", true: "/voice"}[voice], func(t *testing.T) {
				w := newWorkPipeline(t, tc.cfg.Clone(), "Sure.")
				w.p.RouterToolsPolicy, w.p.RouteJSON = tc.router, tc.router == brain.RouterToolsRouteOnly
				var err error
				if voice {
					w.p.STT = &fakeSTT{text: "Look into the build failure."}
					_, err = w.p.RunTurn(t.Context())
				} else {
					err = w.p.RunTurnTextMode(t.Context(), "Look into the build failure.")
				}
				if err != nil {
					t.Fatal(err)
				}
				checkInlineTurn(t, w, tc.want)
			})
		}
	}
	if w := newWorkPipeline(t, workPersona(config.WorkWorker, true), "Sure."); !w.p.backgroundWorkerAvailable() {
		t.Fatal("a persona set to worker lost its hand-off")
	}
}

func checkInlineTurn(t *testing.T, w *workPipeline, want brain.RouterToolsPolicy) {
	t.Helper()
	if len(w.b.opts) != 1 {
		t.Fatalf("model ran %d time(s)", len(w.b.opts))
	}
	opts := w.b.opts[0]
	if opts.RouterToolsPolicy != want || !opts.ToolsEnabled {
		t.Fatalf("policy %q tools %v, want %q", opts.RouterToolsPolicy, opts.ToolsEnabled, want)
	}
	if want == brain.RouterToolsDirect && (opts.BackgroundWorker || opts.WorkerTiers != nil || opts.RouteJSON || w.p.routeContext().TieredWorkers) {
		t.Fatalf("inline turn can still hand off: %+v", opts)
	}
}

// A spoken tool promise from an inline persona is its reply, not a job.
func TestInlinePersonaDoesNotRepairASpokenPromiseIntoAJob(t *testing.T) {
	const promise = "I am invoking the tool now to retrieve only those records."
	for _, tools := range []bool{true, false} {
		w := newWorkPipeline(t, workPersona(config.WorkInline, tools), promise)
		var final string
		events.Subscribe(w.p.Events, func(e events.ResponseReady) { final = e.Response })
		if err := w.p.RunTurnTextMode(t.Context(), "filter design work items"); err != nil {
			t.Fatal(err)
		}
		if w.spawned() || final != promise {
			t.Fatalf("tools %v: spawned %v, reply %q", tools, w.spawned(), final)
		}
	}
}

func TestInlinePersonaTellsALongStoryItself(t *testing.T) {
	if kind, _, ok := classifyTurn("Tell me a long story.", RouteContext{InlineWork: true}); ok {
		t.Fatalf("inline long story routed %q", kind)
	}
	if kind, _, _ := classifyTurn("Tell me a long story.", RouteContext{}); kind != RouteNarrate {
		t.Fatalf("long story routed %q, want narrate", kind)
	}
}

// Any spawn route that still reaches an inline persona starts no job and is
// answered: the reason is shown and a line is spoken. A moved reply's hand-off
// is refused the same way.
func TestSpawnRouteForAnInlinePersonaStartsNoJob(t *testing.T) {
	h := newOwnHarness(t, workPersona(config.WorkInline, true), nil)
	metrics := newTurnMetrics()
	spoken := []string{}
	metrics.routedSpeech = &spoken
	h.a.HandleRoute(t.Context(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "tidy the notes", Source: "test"})

	select {
	case r := <-h.routes:
		if r.State != "rejected" || !strings.Contains(r.Message, "Lumen does its work in the conversation") {
			t.Fatalf("route = %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no route decision")
	}
	select {
	case msg := <-h.info:
		if !strings.Contains(msg, "starts no background jobs") {
			t.Fatalf("info = %q", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the refusal was not shown")
	}
	if len(spoken) != 1 || spoken[0] != inlineSpawnLine {
		t.Fatalf("spoken = %q", spoken)
	}
	if n := len(h.jobs.Status()); n != 0 || h.configured.calls() != 0 || len(h.builtConfigs()) != 0 {
		t.Fatalf("a job ran: jobs %d, worker %d, own %d", n, h.configured.calls(), len(h.builtConfigs()))
	}
	if _, err := h.a.admitSpawn(t.Context(), "", h.a.workerSource()); err == nil || !strings.Contains(err.Error(), "starts no background jobs") {
		t.Fatalf("admitSpawn = %v", err)
	}
}

// The story writer is a job too: a narrate route for a long story from an
// inline persona is answered the same way and starts nothing.
func TestLongStoryRouteForAnInlinePersonaStartsNoJob(t *testing.T) {
	h := newOwnHarness(t, workPersona(config.WorkInline, true), nil)
	metrics := newTurnMetrics()
	spoken := []string{}
	metrics.routedSpeech = &spoken
	h.a.HandleRoute(t.Context(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteNarrate, Transcript: "Tell me a long story.", Source: "test"})
	if len(spoken) != 1 || spoken[0] != inlineSpawnLine || len(h.jobs.Status()) != 0 {
		t.Fatalf("spoken %q, jobs %+v", spoken, h.jobs.Status())
	}
}
