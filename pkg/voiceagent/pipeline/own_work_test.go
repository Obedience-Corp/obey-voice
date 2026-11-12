package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	reply     string
	release   <-chan struct{}
	mu        sync.Mutex
	briefs    []string
	opts      []brain.StreamOptions
	deadlines []time.Time
}

func (w *recordingWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}

func (w *recordingWorker) ThinkFull(ctx context.Context, brief string, opts brain.StreamOptions) (string, error) {
	deadline, _ := brain.WorkerDeadline(ctx)
	w.mu.Lock()
	w.briefs = append(w.briefs, brief)
	w.opts = append(w.opts, opts)
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	if opts.OnWorkerUsage != nil {
		in, out := 120, 40
		opts.OnWorkerUsage(brain.WorkerUsage{InputTokens: &in, OutputTokens: &out})
	}
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
	errs       chan string
	usage      chan events.WorkerUsageRecorded
	routes     chan events.RouteDecided
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
		errs:       make(chan string, 16),
		usage:      make(chan events.WorkerUsageRecorded, 4),
		routes:     make(chan events.RouteDecided, 4),
	}
	h.p = &Pipeline{Events: events.NewBus(), Brain: &lifecycleWorker{reply: "live"}}
	h.p.SetOutputMuted(true)
	events.Subscribe(h.p.Events, func(e events.JobFinished) { h.finished <- e })
	events.Subscribe(h.p.Events, func(e events.JobFailed) { h.failed <- e })
	events.Subscribe(h.p.Events, func(e events.JobAborted) { h.aborted <- e })
	events.Subscribe(h.p.Events, func(e events.Info) { h.info <- e.Message })
	events.Subscribe(h.p.Events, func(e events.Error) { h.errs <- e.Message })
	events.Subscribe(h.p.Events, func(e events.WorkerUsageRecorded) { h.usage <- e })
	events.Subscribe(h.p.Events, func(e events.RouteDecided) { h.routes <- e })
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

// tieredPersona is Mira on provider in tiered mode, with the simple tier
// assigned to a local worker and the medium tier disabled.
func tieredPersona(provider, work string, tools bool) *config.Config {
	cfg := tierTestConfig()
	cfg.ActivePersona, cfg.AgentName, cfg.BrainProvider, cfg.PersonaWork, cfg.VoiceToolsEnabled = "mira", "Mira", provider, work, tools
	cfg.ClaudeModel, cfg.OllamaModel = "sonnet", "persona:14b"
	cfg.VoiceWorkers.Tiers.Simple.Limits = config.WorkerLimits{RuntimeSeconds: 45, ModelRequests: 3, ToolCalls: 4, TransportRetries: 0}
	cfg.VoiceWorkers.Tiers.Medium.Limits.RuntimeSeconds = 90
	return cfg
}

func strictPolicy(cfg *config.Config) *config.Config {
	daily := int64(1_000_000)
	cfg.VoiceWorkers.Policy.ChargePolicy, cfg.VoiceWorkers.Policy.DailyChargeMicroUSD = "strict", &daily
	return cfg
}

func (h *ownHarness) ownCall(t *testing.T) (brain.StreamOptions, time.Time) {
	t.Helper()
	h.own.mu.Lock()
	defer h.own.mu.Unlock()
	if len(h.own.opts) != 1 {
		t.Fatalf("own backend ran %d job(s), want 1", len(h.own.opts))
	}
	return h.own.opts[0], h.own.deadlines[0]
}

func (h *ownHarness) awaitRejected(t *testing.T, want string) {
	t.Helper()
	select {
	case msg := <-h.errs:
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want %q", msg, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no error was reported")
	}
	select {
	case r := <-h.routes:
		if r.State != "rejected" || !strings.Contains(r.Message, want) {
			t.Fatalf("route = %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no route decision")
	}
	if n := len(h.jobs.Status()); n != 0 || len(h.builtConfigs()) != 0 || h.configured.calls() != 0 || h.planned != 0 {
		t.Fatalf("a refused job ran: jobs %d own %d configured %d planned %d", n, len(h.builtConfigs()), h.configured.calls(), h.planned)
	}
}

func TestOwnWorkInTieredModeIsHeldToTheRoutedTier(t *testing.T) {
	t.Run("tools off stays off", func(t *testing.T) {
		h := newOwnHarness(t, tieredPersona("claude", "", false), nil, job.WithBudget(time.Hour))
		h.spawn("refactor the parser", config.WorkerTierSimple)
		h.awaitFinished(t)
		opts, _ := h.ownCall(t)
		got := h.builtConfigs()[0]
		if opts.ToolsEnabled || got.VoiceToolsEnabled || tieredWorkers(got) || got.VoiceWorkerProvider != "claude" || got.VoiceWorkerModel != "sonnet" || h.planned != 0 || h.configured.calls() != 0 {
			t.Fatalf("own job: tools %v (config %v) tiered %v worker %s/%s planned %d configured %d",
				opts.ToolsEnabled, got.VoiceToolsEnabled, tieredWorkers(got), got.VoiceWorkerProvider, got.VoiceWorkerModel, h.planned, h.configured.calls())
		}
		h.a.mu.Lock()
		admission := h.a.workerOrigins[h.jobs.Status()[0].ID].admission
		h.a.mu.Unlock()
		if admission["tools"] != "false" || admission["worker_tier"] != "simple" || admission["work"] != config.WorkOwn {
			t.Fatalf("admission = %v", admission)
		}
	})
	t.Run("tools on", func(t *testing.T) {
		h := newOwnHarness(t, tieredPersona("claude", "", true), nil, job.WithBudget(time.Hour))
		h.spawn("refactor the parser", config.WorkerTierSimple)
		h.awaitFinished(t)
		if opts, _ := h.ownCall(t); !opts.ToolsEnabled || opts.WorkerBudget != nil {
			t.Fatalf("own job on claude: tools %v budget %v, want tools and the runtime limit only", opts.ToolsEnabled, opts.WorkerBudget)
		}
	})
	t.Run("the routed tier's runtime, not longer than the job budget", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			tier  config.WorkerTierName
			table time.Duration
			want  time.Duration
		}{
			{"simple", config.WorkerTierSimple, time.Hour, 45 * time.Second},
			{"a shorter job budget wins", config.WorkerTierSimple, 20 * time.Second, 20 * time.Second},
			{"the hosted default when the budget is unset", config.WorkerTierSimple, 0, 45 * time.Second},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newOwnHarness(t, tieredPersona("claude", "", true), nil, job.WithBudget(tc.table))
				start := time.Now()
				h.spawn("refactor the parser", tc.tier)
				h.awaitFinished(t)
				_, deadline := h.ownCall(t)
				if got := deadline.Sub(start); got > tc.want+time.Second || got < tc.want-time.Second {
					t.Fatalf("own job deadline in %s, want %s", got, tc.want)
				}
			})
		}
	})
	t.Run("a local own job gets the tier's allowances and the strict allowlist", func(t *testing.T) {
		h := newOwnHarness(t, strictPolicy(tieredPersona("ollama", config.WorkOwn, true)), nil, job.WithBudget(time.Hour))
		h.spawn("tidy the notes", config.WorkerTierSimple)
		h.awaitFinished(t)
		opts, _ := h.ownCall(t)
		if opts.WorkerBudget == nil {
			t.Fatal("a local own job ran without the tier's budget")
		}
		limits := opts.WorkerBudget.Snapshot().Limits
		if !limits.StrictTools || limits.MaxRequests != 3 || limits.MaxToolCalls != 4 || limits.MaxRetries != 0 || limits.MaxRuntime != 45*time.Second {
			t.Fatalf("budget limits = %+v", limits)
		}
		if err := opts.WorkerBudget.AllowTool("run_command"); err == nil {
			t.Fatal("the strict allowlist let run_command through")
		}
		if err := opts.WorkerBudget.AllowTool("read_file"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a hosted own job is refused under the strict policy", func(t *testing.T) {
		for _, provider := range []string{"claude", "codex", "cursor", "grok", "muse-spark", "obey"} {
			for _, tools := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s tools %v", provider, tools), func(t *testing.T) {
					h := newOwnHarness(t, strictPolicy(tieredPersona(provider, "", tools)), nil)
					h.spawn("refactor the parser", config.WorkerTierSimple)
					h.awaitRejected(t, "Mira cannot do its own work under the strict worker policy: "+provider+" runs its own tools")
				})
			}
		}
	})
	t.Run("a disabled tier is refused", func(t *testing.T) {
		h := newOwnHarness(t, tieredPersona("claude", "", true), nil)
		h.spawn("refactor the parser", config.WorkerTierMedium)
		h.awaitRejected(t, "worker tier medium is disabled")
	})
	t.Run("with every tier disabled, own work is refused and not advertised", func(t *testing.T) {
		cfg := tieredPersona("claude", "", true)
		cfg.VoiceWorkers.Tiers.Simple.Enabled = false
		h := newOwnHarness(t, cfg, nil)
		if refused := h.a.resolveWork().refused; refused == nil || !strings.Contains(refused.Error(), "none is enabled") {
			t.Fatalf("refused = %v", refused)
		}
		h.spawn("refactor the parser", config.WorkerTierSimple)
		h.awaitRejected(t, "worker tiers are on and none is enabled")
	})
	t.Run("a job routed to no tier is refused", func(t *testing.T) {
		h := newOwnHarness(t, tieredPersona("claude", "", true), nil)
		h.spawn("refactor the parser", "")
		h.awaitRejected(t, "routed to no known tier")
	})
	t.Run("work handed to the worker still uses the tier's assignment", func(t *testing.T) {
		w := newOwnHarness(t, tieredPersona("claude", config.WorkWorker, true), nil)
		w.spawn("refactor the parser", config.WorkerTierSimple)
		w.awaitFinished(t)
		if w.planned != 1 || len(w.builtConfigs()) != 0 {
			t.Fatalf("worker route: tier plan built %d, own built %d", w.planned, len(w.builtConfigs()))
		}
	})
}

func TestOwnWorkUnderTheStrictPolicyIsNotAdvertisedOnAHostedBackend(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"advisory", tieredPersona("claude", "", true), true},
		{"strict", strictPolicy(tieredPersona("claude", "", true)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOwnHarness(t, tc.cfg, nil)
			b := &harnessFixtureBrain{routedProjectionBrain: &routedProjectionBrain{fakeBrain: &fakeBrain{chunks: []string{"Sure."}}}}
			b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
				return brain.StreamResult{}, nil
			}
			h.p.Brain = b
			if _, err := h.p.startBrainStream(t.Context(), "refactor the parser", brain.StreamOptions{ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly}); err != nil {
				t.Fatal(err)
			}
			if routing := b.chat[0].HarnessRouting; routing == nil || routing.WorkersAvailable != tc.want {
				t.Fatalf("harness routing = %+v, want work available %v", routing, tc.want)
			}
		})
	}
}

func TestOwnWorkReportsItsUsage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      *config.Config
		tier     config.WorkerTierName
		counters bool
	}{
		{"hosted", claudePersona(), "", false},
		{"local in tiered mode", tieredPersona("ollama", config.WorkOwn, true), config.WorkerTierSimple, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOwnHarness(t, tc.cfg, nil, job.WithBudget(time.Hour))
			h.spawn("tidy the notes", tc.tier)
			h.awaitFinished(t)
			select {
			case u := <-h.usage:
				if u.JobID != h.jobs.Status()[0].ID || u.InputTokens == nil || *u.InputTokens != 120 || u.OutputTokens == nil || *u.OutputTokens != 40 || (u.Requests != nil) != tc.counters {
					t.Fatalf("usage = %+v", u)
				}
			default:
				t.Fatal("the own job recorded no usage")
			}
		})
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
	t.Run("budget in tiered mode", func(t *testing.T) {
		h := newOwnHarness(t, tieredPersona("claude", "", true), nil, job.WithBudget(60*time.Millisecond))
		release := make(chan struct{})
		defer close(release)
		h.own.release = release
		h.spawn("audit everything", config.WorkerTierSimple)
		select {
		case e := <-h.aborted:
			if e.Reason != events.AbortBudget {
				t.Fatalf("reason = %q", e.Reason)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("voice_worker_job_budget never stopped the own job in tiered mode")
		}
		if len(h.builtConfigs()) != 1 || h.planned != 0 {
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
	if h.a.resolveWork().own != nil {
		t.Fatal("experimental work chose the hosted backend")
	}
	local := claudePersona()
	local.BrainProvider, local.OllamaModel, local.PersonaWork = "ollama", "persona:14b", config.WorkOwn
	h.a.ReplaceWorker(h.configured, local, nil)
	if got := h.a.resolveWork().own; got == nil || got.VoiceWorkerProvider != "ollama" || got.VoiceWorkerModel != "persona:14b" {
		t.Fatalf("an Ollama persona's own work under the experimental inbox = %+v", got)
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

// The result waits for the turn in progress, then is spoken in the voice the
// persona had when it took the work, even after another voice took over.
func TestOwnWorkResultIsSpokenInThePersonasVoiceAtTheNextPause(t *testing.T) {
	mira, ember := &fakeTTS{}, &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	own := &slowWorker{started: make(chan struct{}), done: make(chan struct{}), reply: "The failing test passes now."}
	p := &Pipeline{TTS: mira, Player: player, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("own-voice"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	configured := &recordingWorker{reply: "The configured worker did it."}
	a := NewAgency(p, jobs, configured, claudePersona(), nil)
	a.ownFactory = func(context.Context, *config.Config) (brain.Provider, error) { return own, nil }
	a.Wire()
	finished := make(chan struct{}, 1)
	jobs.OnDone(func(id string, state job.State, result string, err error) {
		a.onJobDone(id, state, result, err)
		finished <- struct{}{}
	})
	var speaker string
	events.Subscribe(p.Events, func(e events.SpeechSegmentReady) {
		if e.Text == own.reply && e.Envelope != nil {
			speaker = e.Envelope.PersonaID
		}
	})
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{PersonaID: "mira", DisplayName: "Mira"}
	a.handleSpawn(t.Context(), p.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteSpawn, Transcript: "fix the failing test"})
	select {
	case <-own.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the persona's own backend never started the job")
	}
	p.ReplaceTTS(ember, nil)
	p.incActiveTurn()
	close(own.done)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("missing job completion")
	}
	if strings.Contains(strings.Join(mira.Texts(), " "), own.reply) {
		t.Fatal("the result was spoken over a turn in progress")
	}
	p.decActiveTurn()
	if !strings.Contains(strings.Join(mira.Texts(), " "), own.reply) || len(ember.Texts()) != 0 || speaker != "mira" {
		t.Fatalf("mira=%q ember=%q speaker=%q", mira.Texts(), ember.Texts(), speaker)
	}
	if configured.calls() != 0 {
		t.Fatal("the configured worker ran the persona's job")
	}
}

// harnessEditBrain routes the turn to work after the router was told whether
// work is available, and runs edit in between.
type harnessEditBrain struct {
	*harnessFixtureBrain
	edit func(brain.StreamOptions)
}

func (b *harnessEditBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.edit(opts)
	return b.harnessFixtureBrain.ThinkStream(ctx, input, opts)
}

// A turn spawns on the work decision it was admitted with, so a persona
// edited after the router was told and before the spawn does not flip who
// runs the job.
func TestATurnSpawnsOnTheWorkDecisionItWasAdmittedWith(t *testing.T) {
	for _, tc := range []struct {
		name             string
		admitted, edited string
		own              bool
	}{
		{"own, then edited to worker", config.WorkOwn, config.WorkWorker, true},
		{"worker, then edited to own", config.WorkWorker, config.WorkOwn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := claudePersona()
			cfg.PersonaWork = tc.admitted
			h := newOwnHarness(t, cfg, nil)
			edited := claudePersona()
			edited.PersonaWork = tc.edited
			var advertised *brain.HarnessRouting
			b := &harnessEditBrain{harnessFixtureBrain: &harnessFixtureBrain{routedProjectionBrain: &routedProjectionBrain{fakeBrain: &fakeBrain{route: "spawn"}}}}
			b.classify = func(context.Context, brain.LiveRouteRequest) (brain.StreamResult, error) {
				return brain.StreamResult{}, nil
			}
			b.edit = func(opts brain.StreamOptions) {
				advertised = opts.HarnessRouting
				h.a.ReplaceWorker(h.configured, edited, nil)
			}
			h.p.Brain = b
			h.p.VoiceToolsEnabled = true
			if err := h.p.RunTurnTextMode(t.Context(), "refactor the parser"); err != nil {
				t.Fatal(err)
			}
			done := h.awaitFinished(t)
			if advertised == nil || !advertised.WorkersAvailable {
				t.Fatalf("router was told %+v", advertised)
			}
			if h.a.resolveWork().own != nil == tc.own {
				t.Fatal("the edit did not reach the agency")
			}
			ownRan, configuredRan := len(h.builtConfigs()) == 1, h.configured.calls() == 1
			if ownRan != tc.own || configuredRan == tc.own {
				t.Fatalf("own ran %v, configured ran %v, result %q; want the admitted decision (own %v)", ownRan, configuredRan, done.Summary, tc.own)
			}
		})
	}
}

// storyHarness asks Mira for a long story on an install with no configured
// worker, with Mira's own backend standing in, and records what reached it.
type storyHarness struct {
	a        *Agency
	jobs     *job.Table
	own      *recordingWorker
	speech   *progressiveFakeTTS
	mu       sync.Mutex
	errs     []events.Error
	notices  int
	atBuild  []int
	infoLine []string
}

func newStoryHarness(t *testing.T, cfg *config.Config) *storyHarness {
	cfg.WorkDir = t.TempDir()
	h := &storyHarness{speech: &progressiveFakeTTS{}, own: &recordingWorker{reply: "Chapter One. Once upon a time on Mira's own model.\n\nChapter Two. The end."}}
	player := newFakePlayer(time.Millisecond)
	t.Cleanup(func() { _ = player.Close() })
	h.jobs = job.NewTable(t.Context())
	t.Cleanup(h.jobs.Close)
	p := &Pipeline{TTS: h.speech, Player: player, Events: events.NewBus()}
	p.HostedNotice = func(e events.ResponseEnvelope) (HostedNotice, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if e.PersonaID != "mira" || h.notices > 0 {
			return HostedNotice{}, false
		}
		return HostedNotice{Text: "Mira runs on Claude, a hosted service.", Delivered: func() { h.mu.Lock(); h.notices++; h.mu.Unlock() }}, true
	}
	events.Subscribe(p.Events, func(e events.Error) { h.mu.Lock(); h.errs = append(h.errs, e); h.mu.Unlock() })
	events.Subscribe(p.Events, func(e events.Info) { h.mu.Lock(); h.infoLine = append(h.infoLine, e.Message); h.mu.Unlock() })
	h.a = NewAgency(p, h.jobs, nil, cfg, errors.New("ollama worker model is missing"))
	h.a.ownFactory = func(context.Context, *config.Config) (brain.Provider, error) {
		h.mu.Lock()
		h.atBuild = append(h.atBuild, h.notices)
		h.mu.Unlock()
		return h.own, nil
	}
	h.a.Wire()
	return h
}

func (h *storyHarness) ask(t *testing.T, tier config.WorkerTierName) {
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{ResponseID: "response-1", PersonaID: "mira", DisplayName: "Mira", BrainProvider: "claude", Hosted: true}
	h.a.HandleRoute(t.Context(), h.a.pipeline.newTurnConductor(metrics), metrics, RouteDecision{Kind: RouteNarrate, Transcript: "tell me a long story", Tier: tier, Source: "test"})
}

func (h *storyHarness) awaitSpeech(t *testing.T) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(strings.Join(h.speech.Calls(), " "), "Once upon a time on Mira's own model") {
		select {
		case <-deadline:
			h.mu.Lock()
			defer h.mu.Unlock()
			t.Fatalf("narration never started: speech %v errors %+v", h.speech.Calls(), h.errs)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (h *storyHarness) awaitError(t *testing.T) events.Error {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		if len(h.errs) > 0 {
			e := h.errs[0]
			h.mu.Unlock()
			return e
		}
		h.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("no error")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (h *storyHarness) storyJob(t *testing.T) (job.Info, map[string]string) {
	t.Helper()
	status := h.jobs.Status()
	if len(status) != 1 {
		t.Fatalf("jobs = %+v", status)
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return status[0], h.a.workerOrigins[status[0].ID].admission
}

func TestStoryNarrationWithNoConfiguredWorker(t *testing.T) {
	t.Run("own: the persona's backend writes it, after the hosted notice", func(t *testing.T) {
		h := newStoryHarness(t, claudePersona())
		start := time.Now()
		h.ask(t, "")
		h.awaitSpeech(t)
		h.mu.Lock()
		errs, notices, atBuild := len(h.errs), h.notices, append([]int(nil), h.atBuild...)
		h.mu.Unlock()
		if errs != 0 || notices != 1 || len(atBuild) != 1 || atBuild[0] != 1 {
			t.Fatalf("errors %d, notices %d, notices given when the backend was built %v; want the notice before the story", errs, notices, atBuild)
		}
		h.own.mu.Lock()
		opts, brief, deadline := h.own.opts, h.own.briefs, h.own.deadlines
		h.own.mu.Unlock()
		if len(opts) != 1 || !opts[0].ToolsEnabled || !opts[0].OmitTurnInstruction || brief[0] != storyWriterPrompt {
			t.Fatalf("story job on the own backend = %d call(s), %+v", len(opts), opts)
		}
		if got, want := deadline[0].Sub(start), 15*time.Minute; got > want+time.Second || got < want-5*time.Second {
			t.Fatalf("story deadline in %s, want the hosted own-job default %s", got, want)
		}
		info, admission := h.storyJob(t)
		if info.Provider != "claude" || info.Model != "haiku" || admission["work"] != config.WorkOwn || admission["persona"] != "mira" || admission["provider"] != "claude" {
			t.Fatalf("story job %+v admitted as %v", info, admission)
		}
	})
	t.Run("worker: the missing worker is reported", func(t *testing.T) {
		cfg := claudePersona()
		cfg.PersonaWork = config.WorkWorker
		h := newStoryHarness(t, cfg)
		h.ask(t, "")
		if e := h.awaitError(t); e.Stage != "narrate" || !strings.Contains(e.Message, "ollama worker model is missing") || h.own.calls() != 0 || h.notices != 0 {
			t.Fatalf("error = %+v, own calls %d, notices %d", e, h.own.calls(), h.notices)
		}
	})
}

func TestOllamaPersonaDoesItsOwnWorkWithNoConfiguredWorker(t *testing.T) {
	cfg := &config.Config{ActivePersona: "ember", BrainProvider: "ollama", OllamaModel: "persona:14b", PersonaWork: config.WorkOwn, VoiceToolsEnabled: true, WorkDir: t.TempDir()}
	h := newOwnHarness(t, cfg, errors.New("unsupported voice_worker_provider \"bogus\""))
	h.p.Brain = &fakeBrain{route: "spawn"}
	h.p.VoiceToolsEnabled = true
	var route RouteDecision
	h.p.OnRoute = func(d RouteDecision) { route = d }
	if err := h.p.RunTurnTextMode(t.Context(), "tidy the notes"); err != nil {
		t.Fatal(err)
	}
	if done := h.awaitFinished(t); done.Summary != "I did it myself." {
		t.Fatalf("result = %+v", done)
	}
	if route.Kind != RouteSpawn || route.Source != "router" {
		t.Fatalf("route = %+v, want the model's spawn", route)
	}
	if got := h.builtConfigs(); len(got) != 1 || got[0].VoiceWorkerProvider != "ollama" || got[0].VoiceWorkerModel != "persona:14b" {
		t.Fatalf("own backend = %+v", got)
	}
}

// A story in tiered mode is held to its routed tier, as any own job is. A
// story phrase names no tier, so it is refused; a story routed to a tier is
// written under it.
func TestStoryNarrationInTieredModeIsHeldToTheRoutedTier(t *testing.T) {
	t.Run("no tier", func(t *testing.T) {
		h := newStoryHarness(t, tieredPersona("claude", "", true))
		h.ask(t, "")
		if e := h.awaitError(t); e.Stage != "job" || !strings.Contains(e.Message, "routed to no known tier") || h.own.calls() != 0 || h.notices != 0 || len(h.jobs.Status()) != 0 {
			t.Fatalf("error = %+v, own calls %d, notices %d, jobs %d", e, h.own.calls(), h.notices, len(h.jobs.Status()))
		}
	})
	t.Run("routed to a tier", func(t *testing.T) {
		h := newStoryHarness(t, tieredPersona("claude", "", true))
		start := time.Now()
		h.ask(t, config.WorkerTierSimple)
		h.awaitSpeech(t)
		h.own.mu.Lock()
		deadline := h.own.deadlines[0]
		h.own.mu.Unlock()
		if got := deadline.Sub(start); got > 46*time.Second || got < 40*time.Second {
			t.Fatalf("story deadline in %s, want the simple tier's 45s", got)
		}
		if _, admission := h.storyJob(t); admission["worker_tier"] != "simple" || admission["work"] != config.WorkOwn {
			t.Fatalf("admission = %v", admission)
		}
	})
	t.Run("strict policy", func(t *testing.T) {
		h := newStoryHarness(t, strictPolicy(tieredPersona("claude", "", true)))
		h.ask(t, config.WorkerTierSimple)
		if e := h.awaitError(t); !strings.Contains(e.Message, "strict worker policy") || h.own.calls() != 0 {
			t.Fatalf("error = %+v, own calls %d", e, h.own.calls())
		}
	})
}

// A hosted persona's own job is capped at the hosted default while
// voice_worker_job_budget is unset; a set budget wins, -1 lifts the cap, and
// a local own job keeps the job table's budget.
func TestHostedOwnJobDefaultTimeCap(t *testing.T) {
	t.Run("budget unset: cut off at the default", func(t *testing.T) {
		h := newOwnHarness(t, claudePersona(), nil)
		h.a.ownJobDefault = 80 * time.Millisecond
		release := make(chan struct{})
		defer close(release)
		h.own.release = release
		h.spawn("refactor the parser", "")
		select {
		case e := <-h.aborted:
			if e.Reason != events.AbortBudget {
				t.Fatalf("reason = %q", e.Reason)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the hosted default never stopped the own job")
		}
	})
	for _, tc := range []struct {
		name   string
		cfg    func() *config.Config
		table  time.Duration
		budget int
		want   time.Duration
	}{
		{"budget unset: the default", claudePersona, 0, 0, 15 * time.Minute},
		{"a set budget wins, even a longer one", claudePersona, time.Hour, 3600, time.Hour},
		{"unlimited: no cap", claudePersona, 0, config.UnlimitedJobBudget, 0},
		{"a local own job keeps the table's budget", func() *config.Config {
			cfg := claudePersona()
			cfg.BrainProvider, cfg.OllamaModel, cfg.PersonaWork = "ollama", "persona:14b", config.WorkOwn
			return cfg
		}, 0, 0, 0},
		{"in tiered mode a shorter default wins over the tier", func() *config.Config { return tieredPersona("claude", "", true) }, 0, 0, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg()
			cfg.VoiceWorkerJobBudget = tc.budget
			h := newOwnHarness(t, cfg, nil, job.WithBudget(tc.table))
			if tiered := tieredWorkers(cfg); tiered {
				h.a.ownJobDefault = 30 * time.Second
			}
			start := time.Now()
			h.spawn("refactor the parser", config.WorkerTierSimple)
			h.awaitFinished(t)
			_, deadline := h.ownCall(t)
			if tc.want == 0 {
				if !deadline.IsZero() {
					t.Fatalf("own job has a deadline in %s, want none", deadline.Sub(start))
				}
				return
			}
			if got := deadline.Sub(start); got > tc.want+time.Second || got < tc.want-5*time.Second {
				t.Fatalf("own job deadline in %s, want %s", got, tc.want)
			}
		})
	}
}

// The first own job of a hosted persona on an install whose Settings chose a
// worker says, once, where its jobs now run and how to send them back.
func TestOwnWorkDefaultNoticeIsShownOncePerPersona(t *testing.T) {
	const notice = "does its background jobs on its own backend, claude, not on the worker chosen in Settings. To hand them to that worker: obey-voice persona edit mira --work worker"
	shown := func(h *ownHarness) int {
		n := 0
		for {
			select {
			case msg := <-h.info:
				if strings.Contains(msg, notice) {
					n++
				}
			default:
				return n
			}
		}
	}
	run := func(t *testing.T, cfg *config.Config, notices OneTimeNotices) int {
		h := newOwnHarness(t, cfg, nil)
		if notices != nil {
			h.a.UseNotices(notices)
		}
		total := 0
		for range 2 {
			h.spawn("refactor the parser", "")
			h.awaitFinished(t)
			total += shown(h)
		}
		h.own.mu.Lock()
		defer h.own.mu.Unlock()
		for _, brief := range h.own.briefs {
			if strings.Contains(brief, "chosen in Settings") {
				t.Fatal("the notice reached the model")
			}
		}
		return total
	}
	t.Run("once, and not again after a restart", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "notices.json")
		if n := run(t, claudePersona(), config.NewNotices(path)); n != 1 {
			t.Fatalf("notice shown %d times in the first session, want 1", n)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("notice not recorded: %v", err)
		}
		if n := run(t, claudePersona(), config.NewNotices(path)); n != 0 {
			t.Fatalf("notice shown %d times after a restart", n)
		}
	})
	t.Run("never without a worker in Settings", func(t *testing.T) {
		cfg := claudePersona()
		cfg.VoiceWorkerProvider, cfg.VoiceWorkerModel = "", ""
		if n := run(t, cfg, nil); n != 0 {
			t.Fatalf("notice shown %d times", n)
		}
	})
	t.Run("never for a persona that set its work", func(t *testing.T) {
		cfg := claudePersona()
		cfg.PersonaWork = config.WorkOwn
		if n := run(t, cfg, nil); n != 0 {
			t.Fatalf("notice shown %d times", n)
		}
	})
}
