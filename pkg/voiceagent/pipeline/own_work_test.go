package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

// recordingWorker answers every job with reply and keeps what it was given.
// A non-nil release holds each job until it closes or the job is stopped.
type recordingWorker struct {
	reply   string
	release <-chan struct{}
	mu      sync.Mutex
	briefs  []string
	opts    []brain.StreamOptions
}

func (w *recordingWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}

func (w *recordingWorker) ThinkFull(ctx context.Context, brief string, opts brain.StreamOptions) (string, error) {
	w.mu.Lock()
	w.briefs = append(w.briefs, brief)
	w.opts = append(w.opts, opts)
	w.mu.Unlock()
	if w.release != nil {
		select {
		case <-w.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return w.reply, nil
}

func (w *recordingWorker) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.briefs)
}

func (w *recordingWorker) ClearHistory()            {}
func (w *recordingWorker) History() []brain.Turn    { return nil }
func (w *recordingWorker) LoadHistory([]brain.Turn) {}

// ownHarness is an Agency bound to one persona, with a configured worker and
// a factory standing in for the persona's own backend.
type ownHarness struct {
	p          *Pipeline
	a          *Agency
	jobs       *job.Table
	configured *recordingWorker
	own        *recordingWorker
	planned    int
	mu         sync.Mutex
	built      []*config.Config
	buildErr   error
	finished   chan events.JobFinished
	failed     chan events.JobFailed
	aborted    chan events.JobAborted
	info       chan string
}

func newOwnHarness(t *testing.T, cfg *config.Config, workerErr error, opts ...job.Option) *ownHarness {
	t.Helper()
	h := &ownHarness{
		configured: &recordingWorker{reply: "The configured worker did it."},
		own:        &recordingWorker{reply: "I did it myself."},
		finished:   make(chan events.JobFinished, 4),
		failed:     make(chan events.JobFailed, 4),
		aborted:    make(chan events.JobAborted, 4),
		info:       make(chan string, 16),
	}
	h.p = &Pipeline{Events: events.NewBus(), Brain: &lifecycleWorker{reply: "live"}}
	h.p.SetOutputMuted(true)
	events.Subscribe(h.p.Events, func(e events.JobFinished) { h.finished <- e })
	events.Subscribe(h.p.Events, func(e events.JobFailed) { h.failed <- e })
	events.Subscribe(h.p.Events, func(e events.JobAborted) { h.aborted <- e })
	events.Subscribe(h.p.Events, func(e events.Info) { h.info <- e.Message })
	h.jobs = job.NewTable(context.Background(), opts...)
	t.Cleanup(h.jobs.Close)
	var configured brain.Provider
	if workerErr == nil {
		configured = h.configured
	}
	h.a = NewAgency(h.p, h.jobs, configured, cfg, workerErr)
	h.a.Wire()
	h.a.ownFactory = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.built = append(h.built, cfg.Clone())
		if h.buildErr != nil {
			return nil, h.buildErr
		}
		return h.own, nil
	}
	h.a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		h.mu.Lock()
		h.planned++
		h.mu.Unlock()
		return h.configured, nil
	}
	return h
}

func (h *ownHarness) spawn(task string, tier config.WorkerTierName) {
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{ResponseID: "response-1", PersonaID: "mira", DisplayName: "Mira", Voice: "mira-voice", BrainProvider: "claude", Hosted: true}
	h.a.HandleRoute(context.Background(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: task, Tier: tier, Source: "test"})
}

func (h *ownHarness) builtConfigs() []*config.Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*config.Config(nil), h.built...)
}

func (h *ownHarness) awaitFinished(t *testing.T) events.JobFinished {
	t.Helper()
	select {
	case e := <-h.finished:
		return e
	case e := <-h.failed:
		t.Fatalf("job failed: %+v", e)
	case <-time.After(3 * time.Second):
		t.Fatalf("job did not finish: %+v", h.jobs.Status())
	}
	return events.JobFinished{}
}

func claudePersona() *config.Config {
	return &config.Config{ActivePersona: "mira", BrainProvider: "claude", ClaudeModel: "haiku", WorkDir: "/work/mira",
		VoiceToolsEnabled: true, VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker:8b", VoiceWorkerOllamaHost: "http://worker:11434"}
}

func TestPersonaOnAHarnessDoesItsOwnWorkOnItsOwnModel(t *testing.T) {
	h := newOwnHarness(t, claudePersona(), nil)
	h.spawn("switch yourself to haiku", "")
	done := h.awaitFinished(t)
	if done.Summary != "I did it myself." || done.Envelope == nil || done.Envelope.PersonaID != "mira" || done.Envelope.Voice != "mira-voice" {
		t.Fatalf("result = %+v, want the persona's own answer under its name and voice", done)
	}
	if h.configured.calls() != 0 {
		t.Fatalf("the configured worker ran %d job(s)", h.configured.calls())
	}
	built := h.builtConfigs()
	if len(built) != 1 {
		t.Fatalf("own backend built %d times", len(built))
	}
	got := built[0]
	if got.BrainProvider != "claude" || got.VoiceWorkerProvider != "claude" || got.VoiceWorkerModel != "haiku" || got.ClaudeModel != "haiku" ||
		got.VoiceWorkerOllamaHost != "" || got.WorkDir != "/work/mira" || got.ActivePersona != "mira" {
		t.Fatalf("own job config = provider %q worker %q/%q host %q dir %q persona %q", got.BrainProvider, got.VoiceWorkerProvider, got.VoiceWorkerModel, got.VoiceWorkerOllamaHost, got.WorkDir, got.ActivePersona)
	}
	status := h.jobs.Status()
	if len(status) != 1 || status[0].Provider != "claude" || status[0].Model != "haiku" {
		t.Fatalf("job status = %+v, want claude/haiku", status)
	}
	h.a.mu.Lock()
	admission := h.a.workerOrigins[status[0].ID].admission
	h.a.mu.Unlock()
	if admission["work"] != config.WorkOwn || admission["persona"] != "mira" || admission["provider"] != "claude" {
		t.Fatalf("admission = %v", admission)
	}
	h.own.mu.Lock()
	brief, opts := h.own.briefs[0], h.own.opts[0]
	h.own.mu.Unlock()
	if !strings.Contains(brief, "switch yourself to haiku") || !strings.Contains(brief, `persona with id "mira"`) {
		t.Fatalf("brief = %q", brief)
	}
	if !opts.ToolsEnabled || !opts.OmitTurnInstruction || opts.RouterToolsPolicy != brain.RouterToolsLegacy {
		t.Fatalf("own job options = %+v, want the worker tool surface", opts)
	}
}

func TestPersonaOnAHarnessSetToWorkerHandsWorkToTheConfiguredWorker(t *testing.T) {
	cfg := claudePersona()
	cfg.PersonaWork = config.WorkWorker
	h := newOwnHarness(t, cfg, nil)
	h.spawn("run the tests", "")
	if done := h.awaitFinished(t); done.Summary != "The configured worker did it." {
		t.Fatalf("result = %+v", done)
	}
	if n := len(h.builtConfigs()); n != 0 || h.configured.calls() != 1 {
		t.Fatalf("own built %d, configured ran %d", n, h.configured.calls())
	}
	if status := h.jobs.Status(); status[0].Provider != "ollama" || status[0].Model != "worker:8b" {
		t.Fatalf("job status = %+v", status)
	}
}

func TestPersonaOnOllamaChoosesBetweenTheWorkerAndItsOwnModel(t *testing.T) {
	persona := func(work string) *config.Config {
		return &config.Config{ActivePersona: "ember", BrainProvider: "ollama", OllamaModel: "persona:14b", OllamaHost: "http://persona:11434", OllamaNumCtx: 16384,
			PersonaWork: work, WorkDir: "/work/ember", VoiceToolsEnabled: true,
			VoiceWorkerProvider: "ollama", VoiceWorkerModel: "worker:8b", VoiceWorkerOllamaHost: "http://worker:11434", VoiceWorkerNumCtx: 65536}
	}
	t.Run("default is the worker", func(t *testing.T) {
		h := newOwnHarness(t, persona(""), nil)
		h.spawn("tidy the notes", "")
		if done := h.awaitFinished(t); done.Summary != "The configured worker did it." || len(h.builtConfigs()) != 0 {
			t.Fatalf("result = %+v, own built %d", done, len(h.builtConfigs()))
		}
	})
	t.Run("own runs on its model", func(t *testing.T) {
		h := newOwnHarness(t, persona(config.WorkOwn), nil)
		h.spawn("tidy the notes", "")
		if done := h.awaitFinished(t); done.Summary != "I did it myself." || h.configured.calls() != 0 {
			t.Fatalf("result = %+v, configured ran %d", done, h.configured.calls())
		}
		got := config.WorkerBrainConfig(h.builtConfigs()[0])
		if got.BrainProvider != "ollama" || got.OllamaModel != "persona:14b" || got.OllamaHost != "http://persona:11434" || got.OllamaNumCtx != 16384 || !got.IsWorker {
			t.Fatalf("own worker = %s %s at %s, num_ctx %d, worker %v", got.BrainProvider, got.OllamaModel, got.OllamaHost, got.OllamaNumCtx, got.IsWorker)
		}
	})
}

func TestOwnWorkIgnoresWorkerTiers(t *testing.T) {
	tiered := func(work string) *config.Config {
		cfg := tierTestConfig()
		cfg.ActivePersona, cfg.BrainProvider, cfg.ClaudeModel, cfg.PersonaWork = "mira", "claude", "sonnet", work
		return cfg
	}
	h := newOwnHarness(t, tiered(""), nil, job.WithBudget(time.Hour))
	h.spawn("refactor the parser", config.WorkerTierSimple)
	h.awaitFinished(t)
	if h.planned != 0 || h.configured.calls() != 0 {
		t.Fatalf("tier plan built %d, configured ran %d", h.planned, h.configured.calls())
	}
	got := h.builtConfigs()[0]
	if got.VoiceWorkers != nil || !got.VoiceToolsEnabled || got.VoiceWorkerProvider != "claude" || got.VoiceWorkerModel != "sonnet" {
		t.Fatalf("own job config: tiers %v tools %v worker %s/%s", got.VoiceWorkers, got.VoiceToolsEnabled, got.VoiceWorkerProvider, got.VoiceWorkerModel)
	}
	h.own.mu.Lock()
	tools := h.own.opts[0].ToolsEnabled
	h.own.mu.Unlock()
	if !tools {
		t.Fatal("an own job in tiered mode ran without tools")
	}

	w := newOwnHarness(t, tiered(config.WorkWorker), nil)
	w.spawn("refactor the parser", config.WorkerTierSimple)
	w.awaitFinished(t)
	if w.planned != 1 || len(w.builtConfigs()) != 0 {
		t.Fatalf("worker route: tier plan built %d, own built %d", w.planned, len(w.builtConfigs()))
	}
}

func TestOwnWorkObeysTheJobBudgetAndCancel(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		h := newOwnHarness(t, claudePersona(), nil, job.WithBudget(60*time.Millisecond))
		release := make(chan struct{})
		defer close(release)
		h.own.release = release
		h.spawn("audit everything", "")
		select {
		case e := <-h.aborted:
			if e.Reason != events.AbortBudget {
				t.Fatalf("reason = %q", e.Reason)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the budget never stopped the own job")
		}
		if len(h.builtConfigs()) != 1 {
			t.Fatal("the job was not the persona's own")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		h := newOwnHarness(t, claudePersona(), nil)
		release := make(chan struct{})
		defer close(release)
		h.own.release = release
		h.spawn("audit everything", "")
		deadline := time.Now().Add(3 * time.Second)
		for h.own.calls() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the own job never started")
			}
			time.Sleep(time.Millisecond)
		}
		metrics := newTurnMetrics()
		h.a.HandleRoute(context.Background(), h.p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteJobAbort, Transcript: "cancel", Source: "test"})
		select {
		case e := <-h.aborted:
			if e.Reason != events.AbortCancelled {
				t.Fatalf("reason = %q", e.Reason)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel never stopped the own job")
		}
	})
	t.Run("workers off", func(t *testing.T) {
		cfg := claudePersona()
		cfg.VoiceWorkersDisabled = true
		h := newOwnHarness(t, cfg, nil)
		h.spawn("audit everything", "")
		if n := len(h.jobs.Status()); n != 0 || len(h.builtConfigs()) != 0 {
			t.Fatalf("a job started with background workers off: %d", n)
		}
	})
}

func TestOwnWorkDoesNotNeedAConfiguredWorker(t *testing.T) {
	h := newOwnHarness(t, claudePersona(), errors.New("ollama worker model is missing"))
	h.spawn("fix the failing test", "")
	if done := h.awaitFinished(t); done.Summary != "I did it myself." {
		t.Fatalf("result = %+v", done)
	}
	b := &harnessFixtureBrain{routedProjectionBrain: &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Sure."}}}}
	b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
		return brain.StreamResult{}, nil
	}
	h.p.Brain = b
	if _, err := h.p.startBrainStream(t.Context(), "how are you", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly}); err != nil {
		t.Fatal(err)
	}
	if routing := b.chat[0].HarnessRouting; routing == nil || !routing.WorkersAvailable {
		t.Fatalf("harness routing = %+v, want work available to a persona doing its own", routing)
	}
}

func TestOwnWorkReportsABackendThatCannotBeBuilt(t *testing.T) {
	h := newOwnHarness(t, claudePersona(), nil)
	h.buildErr = errors.New("claude CLI not found on PATH")
	h.spawn("fix the failing test", "")
	select {
	case e := <-h.failed:
		if !strings.Contains(e.Err, "prepare claude for its own work") || !strings.Contains(e.Err, "claude CLI not found") {
			t.Fatalf("failure = %q", e.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the job did not fail")
	}
	if h.configured.calls() != 0 {
		t.Fatal("a failed own job fell back to the configured worker")
	}
}

func TestOwnWorkOnAHostedBackendGivesTheHostedNoticeFirst(t *testing.T) {
	h := newOwnHarness(t, claudePersona(), nil)
	delivered := 0
	h.p.HostedNotice = func(e events.ResponseEnvelope) (HostedNotice, bool) {
		if e.PersonaID != "mira" || delivered > 0 {
			return HostedNotice{}, false
		}
		return HostedNotice{Text: "Mira runs on Claude, a hosted service.", Delivered: func() { delivered++ }}, true
	}
	h.spawn("look up the release notes", "")
	h.awaitFinished(t)
	if delivered != 1 {
		t.Fatalf("notice delivered %d times", delivered)
	}
	select {
	case msg := <-h.info:
		if !strings.Contains(msg, "hosted service") {
			t.Fatalf("first info = %q", msg)
		}
	default:
		t.Fatal("no notice was shown")
	}

	cfg := claudePersona()
	cfg.PersonaWork = config.WorkWorker
	w := newOwnHarness(t, cfg, nil)
	asked := false
	w.p.HostedNotice = func(events.ResponseEnvelope) (HostedNotice, bool) { asked = true; return HostedNotice{}, false }
	w.spawn("look up the release notes", "")
	w.awaitFinished(t)
	if asked {
		t.Fatal("work handed to the configured worker asked for the persona's hosted notice")
	}
}

func TestExperimentalWorkKeepsAHostedPersonasWorkOnTheLocalWorker(t *testing.T) {
	h := newOwnHarness(t, claudePersona(), nil)
	cfg := claudePersona()
	cfg.VoiceWorkEnabled = true
	if err := h.p.EnableConversationWork("own-work", nil, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.p.CloseWork() })
	h.a.ownFactory = func(context.Context, *config.Config) (brain.Provider, error) {
		t.Error("the experimental inbox ran a hosted persona's own backend")
		return nil, errors.New("refused")
	}
	if _, own := h.a.ownWork(); own {
		t.Fatal("experimental work chose the hosted backend")
	}
	local := claudePersona()
	local.BrainProvider, local.OllamaModel, local.PersonaWork = "ollama", "persona:14b", config.WorkOwn
	h.a.ReplaceWorker(h.configured, local, nil)
	if got, own := h.a.ownWork(); !own || got.VoiceWorkerProvider != "ollama" || got.VoiceWorkerModel != "persona:14b" {
		t.Fatalf("an Ollama persona's own work under the experimental inbox = %v %+v", own, got)
	}
}

func TestOwnWorkOnASharedHistoryBackendGetsOnlyThePersonasShare(t *testing.T) {
	live := museSparkBrain(t)
	share := []brain.Turn{{Role: "user", Content: "Mira, plan the trip."}, {Role: "assistant", Content: "Planning it.", PersonaID: "mira", PersonaDisplayName: "Mira"}}
	canonical := append([]brain.Turn{
		{Role: "user", Content: "Ember, my PIN is PRIVATE-4711."},
		{Role: "assistant", Content: "Checking.", PersonaID: "ember", PersonaDisplayName: "Ember", ToolCalls: []brain.TurnToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"TOOLCALLSECRET"}`}}},
		{Role: "tool", ToolName: "read_file", ToolCallID: "c1", Content: "TOOLRESULTSECRET"},
	}, share...)
	if err := live.ImportContinuity(brain.ContinuitySnapshot{Canonical: canonical, Working: share}); err != nil {
		t.Fatal(err)
	}
	h := newOwnHarness(t, &config.Config{ActivePersona: "mira", BrainProvider: "muse-spark", MuseSparkModel: "spark", VoiceToolsEnabled: true}, nil)
	h.p.Brain = live
	h.spawn("Book it.", "")
	h.awaitFinished(t)
	if got := h.builtConfigs(); len(got) != 1 || got[0].VoiceWorkerProvider != "muse-spark" || got[0].VoiceWorkerModel != "spark" {
		t.Fatalf("own backend = %+v", got)
	}
	h.own.mu.Lock()
	brief := h.own.briefs[0]
	h.own.mu.Unlock()
	if !strings.Contains(brief, "Mira, plan the trip.") {
		t.Fatalf("brief lacks the share:\n%s", brief)
	}
	for _, secret := range []string{"PRIVATE-4711", "TOOLCALLSECRET", "TOOLRESULTSECRET", "Checking."} {
		if strings.Contains(brief, secret) {
			t.Fatalf("brief carries %q:\n%s", secret, brief)
		}
	}
}
