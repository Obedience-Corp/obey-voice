package pipeline

import (
	"cmp"
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
		if r.State != "rejected" || r.ReasonCode != events.RouteReasonInlineWork || !strings.Contains(r.Message, "Lumen does its work in the conversation, so it hands none to background jobs") {
			t.Fatalf("route = %+v", r)
		}
		if label := events.RejectedRouteLabel(r); label != "No job started" {
			t.Fatalf("shown as %q", label)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no route decision")
	}
	if len(spoken) != 1 || spoken[0] != inlineSpawnLine {
		t.Fatalf("spoken = %q", spoken)
	}
	if n := len(h.jobs.Status()); n != 0 || h.configured.calls() != 0 || len(h.builtConfigs()) != 0 {
		t.Fatalf("a job ran: jobs %d, worker %d, own %d", n, h.configured.calls(), len(h.builtConfigs()))
	}
	if _, err := h.a.admitSpawn(t.Context(), "", h.a.workerSource()); err == nil || !strings.Contains(err.Error(), "hands none to background jobs") {
		t.Fatalf("admitSpawn = %v", err)
	}
}

// The story writer is a job too: a narrate route for a long story from an
// inline persona is answered the same way, as one route decision, and starts
// nothing; the work inbox's own story refusal does not take its place.
func TestLongStoryRouteForAnInlinePersonaStartsNoJob(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		h := newOwnHarness(t, workPersona(config.WorkInline, true), nil)
		if inbox {
			if err := h.a.EnableConversationWork("inline-chat", nil, workPersona(config.WorkInline, true), nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.a.CloseWork() })
		}
		metrics := newTurnMetrics()
		spoken := []string{}
		metrics.routedSpeech = &spoken
		routed := 0
		h.p.OnRoute = func(RouteDecision) { routed++ }
		h.a.HandleRoute(t.Context(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteNarrate, Transcript: "Tell me a long story.", Source: "test"})
		var decisions []events.RouteDecided
		for len(h.routes) > 0 {
			decisions = append(decisions, <-h.routes)
		}
		if len(decisions) != 1 || decisions[0].State != "rejected" || decisions[0].ReasonCode != events.RouteReasonInlineWork || routed != 1 {
			t.Fatalf("inbox %v: decisions %+v, OnRoute %d", inbox, decisions, routed)
		}
		if len(h.errs) != 0 {
			t.Fatalf("inbox %v: error %q", inbox, <-h.errs)
		}
		if len(spoken) != 1 || spoken[0] != inlineSpawnLine || len(h.jobs.Status()) != 0 {
			t.Fatalf("inbox %v: spoken %q, jobs %+v", inbox, spoken, h.jobs.Status())
		}
	}
}

// Under tiered mode's strict policy an inline persona is refused, as its own
// work would be: its turns go through the worker tiers, and it is told once.
func TestStrictTieredPolicyRefusesInlineWork(t *testing.T) {
	cfg := strictPolicy(tieredPersona("ollama", config.WorkInline, true))
	w := newWorkPipeline(t, cfg, "Sure.")
	var notices []string
	events.Subscribe(w.p.Events, func(e events.Info) { notices = append(notices, e.Message) })
	for range 2 {
		if err := w.p.RunTurnTextMode(t.Context(), "Look into the build failure."); err != nil {
			t.Fatal(err)
		}
	}
	if w.p.worksInline() || w.b.opts[0].RouterToolsPolicy != brain.RouterToolsRouteOnly || !w.p.routeContext().TieredWorkers {
		t.Fatalf("strict tiered inline persona: inline %v, policy %q", w.p.worksInline(), w.b.opts[0].RouterToolsPolicy)
	}
	refused := 0
	for _, n := range notices {
		if strings.Contains(n, "Mira: inline work is refused under the strict worker policy") {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("notices = %q", notices)
	}
	if w := newWorkPipeline(t, tieredPersona("ollama", config.WorkInline, true), "Sure."); !w.p.worksInline() {
		t.Fatal("the advisory policy refused inline work")
	}
}

// Every way a live turn can start a job, taken by one inline persona, starts
// none; the same persona set to worker starts one each time, so no row passes
// for want of a job it could start.
func TestNoEntryPointStartsAJobForAnInlinePersona(t *testing.T) {
	turn := func(input string) func(*testing.T, *workPipeline) bool {
		return func(t *testing.T, w *workPipeline) bool {
			if err := w.p.RunTurnTextMode(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			return w.jobStarted()
		}
	}
	route := func(kind RouteKind, input, source string) func(*testing.T, *workPipeline) bool {
		return func(t *testing.T, w *workPipeline) bool {
			metrics := newTurnMetrics()
			metrics.routedSpeech = &[]string{}
			w.p.Agency.HandleRoute(t.Context(), w.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: kind, Transcript: input, Source: source})
			return w.jobStarted()
		}
	}
	for _, tc := range []struct {
		name  string
		tools bool
		reply string
		route string
		run   func(*testing.T, *workPipeline) bool
	}{
		{name: "picture shortcut", tools: true, run: turn("Show me a picture of a red fox.")},
		{name: "background intent", tools: true, run: turn("Search the web for the tide tables.")},
		{name: "routed intent", run: turn("Search for the release notes.")},
		{name: "long story phrase", tools: true, run: turn("Tell me a long story.")},
		{name: "model hand-off", tools: true, route: "spawn", run: turn("Look into the build failure.")},
		{name: "model story route", tools: true, route: "narrate", run: turn("Tell me a story about a lighthouse.")},
		{name: "promise repair", tools: true, reply: "I am invoking the tool now to retrieve only those records.", run: turn("Check the records for me.")},
		{name: "tiered proposal", tools: true, run: route(RouteSpawn, "Tidy the notes.", "decision")},
		{name: "spawn route", tools: true, run: route(RouteSpawn, "Tidy the notes.", "router")},
		{name: "moved reply hand-off", tools: true, run: func(t *testing.T, w *workPipeline) bool {
			_, err := w.p.Agency.admitSpawn(t.Context(), "", w.p.Agency.workerSource())
			return err == nil
		}},
		{name: "agent_delegate", tools: true, run: func(t *testing.T, w *workPipeline) bool {
			if err := w.p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
				t.Fatal(err)
			}
			return !w.b.opts[0].InlineWork
		}},
	} {
		for _, work := range []string{config.WorkInline, config.WorkWorker} {
			t.Run(tc.name+"/"+work, func(t *testing.T) {
				cfg := workPersona(work, tc.tools)
				cfg.WorkDir = t.TempDir()
				w := newWorkPipeline(t, cfg, cmp.Or(tc.reply, "Sure."))
				w.b.route = tc.route
				if started := tc.run(t, w); started != (work == config.WorkWorker) {
					t.Fatalf("job started %v; jobs %+v", started, w.jobs.Status())
				}
				// The story writer writes into the work directory.
				for deadline := time.Now().Add(5 * time.Second); w.jobs.Active() && time.Now().Before(deadline); {
					time.Sleep(5 * time.Millisecond)
				}
			})
		}
	}
}

// jobStarted reports a job admitted to the table or a spawn route accepted.
func (w *workPipeline) jobStarted() bool {
	accepted := false
	for len(w.routes) > 0 {
		if r := <-w.routes; r.State == "accepted" {
			accepted = true
		}
	}
	return accepted || len(w.jobs.Status()) > 0
}

// A worker policy saved mid-session reaches the next turn: a persona the
// strict policy held to the tiers works inline once the policy is advisory,
// and its brain is told so rather than relying on the settings it was built
// with.
func TestASavedPolicyReachesTheInlineGateOnTheNextTurn(t *testing.T) {
	cfg := strictPolicy(tieredPersona("ollama", config.WorkInline, true))
	w := newWorkPipeline(t, cfg, "Sure.")
	if err := w.p.RunTurnTextMode(t.Context(), "Hello."); err != nil {
		t.Fatal(err)
	}
	advisory := cfg.VoiceWorkers.Clone()
	advisory.Policy.ChargePolicy, advisory.Revision = "advisory", cfg.VoiceWorkers.Revision+1
	if err := w.p.Agency.ApplyWorkerSettings(advisory); err != nil {
		t.Fatal(err)
	}
	if err := w.p.RunTurnTextMode(t.Context(), "Hello again."); err != nil {
		t.Fatal(err)
	}
	strict, inline := w.b.opts[0], w.b.opts[1]
	if strict.InlineWork || strict.RouterToolsPolicy != brain.RouterToolsRouteOnly {
		t.Fatalf("strict turn: inline %v, policy %q", strict.InlineWork, strict.RouterToolsPolicy)
	}
	if !inline.InlineWork || inline.RouterToolsPolicy != brain.RouterToolsDirect || inline.WorkerTiers != nil {
		t.Fatalf("advisory turn: inline %v, policy %q, tiers %+v", inline.InlineWork, inline.RouterToolsPolicy, inline.WorkerTiers)
	}
}
