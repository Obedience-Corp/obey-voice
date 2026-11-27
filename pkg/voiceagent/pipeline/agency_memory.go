package pipeline

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/ollamamem"
)

// memoryRecheckEvery is how often a job held back for memory is rechecked
// while one is.
const memoryRecheckEvery = 10 * time.Second

// localModel is the Ollama host and model a job loads.
type localModel struct {
	host, model string
	loopback    bool
}

// localModelOf is the model a job on provider loads, when that is a model
// on an Ollama host; ok is false for every other backend.
func localModelOf(provider, host, model string) (localModel, bool) {
	if strings.ToLower(strings.TrimSpace(provider)) != "ollama" || strings.TrimSpace(model) == "" {
		return localModel{}, false
	}
	host, loopback := ollamamem.NormalizeHost(host)
	return localModel{host: host, model: ollamamem.NormalizeModel(model), loopback: loopback}, true
}

// workerLocalModel is the local model a job run on cfg's worker loads.
func workerLocalModel(cfg *config.Config) (localModel, bool) {
	if cfg == nil {
		return localModel{}, false
	}
	return localModelOf(providerName(cfg), config.WorkerBrainConfig(cfg).OllamaHost, modelName(cfg))
}

// brainLocalModel is the local model cfg's own brain loads.
func brainLocalModel(cfg *config.Config) (localModel, bool) {
	if cfg == nil {
		return localModel{}, false
	}
	return localModelOf(cfg.BrainProvider, cfg.OllamaHost, config.BrainModelFor(cfg, cfg.BrainProvider))
}

func (m localModel) resource() string {
	return "ollama " + m.host + " " + m.model
}

// localMemory holds back a local Ollama job whose model will not fit beside
// running work, from readings refreshed off the job table's lock.
type localMemory struct {
	monitor *ollamamem.Monitor
	jobs    *job.Table
	ctx     context.Context
	every   time.Duration

	mu      sync.Mutex
	hosts   map[string]bool
	ticking bool
	// denials counts Admit refusals, so the ticker cannot miss one that
	// lands while it decides to stop.
	denials uint64
}

// UseLocalMemory gates local Ollama jobs on free memory read by monitor.
// Its background work ends with ctx or when the returned stop is called.
func (a *Agency) UseLocalMemory(ctx context.Context, monitor *ollamamem.Monitor) (stop func()) {
	if a == nil || a.jobs == nil || monitor == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.memory = &localMemory{monitor: monitor, jobs: a.jobs, ctx: ctx, every: memoryRecheckEvery, hosts: map[string]bool{}}
	a.mu.Unlock()
	return cancel
}

func (a *Agency) localMemory() *localMemory {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.memory
}

// placeJob gives spec the resource key of the local model it loads, and on
// a loopback host the memory gate, refreshed first when other work is
// active. ctx bounds that refresh.
func (a *Agency) placeJob(ctx context.Context, spec *job.Spec, model localModel, ok bool) {
	if !ok {
		return
	}
	spec.Resource = model.resource()
	m := a.localMemory()
	if m == nil || !model.loopback || spec.Immediate {
		return
	}
	m.mu.Lock()
	m.hosts[model.host] = true
	m.mu.Unlock()
	if a.jobs.Active() {
		m.monitor.Refresh(ctx, model.host)
	}
	spec.Admit = func([]job.Info) (bool, string) {
		ok, reason := m.monitor.Decide(model.host, model.model)
		if !ok {
			m.denied()
		}
		return ok, reason
	}
}

// denied runs under the job table's lock, so it only starts the ticker.
func (m *localMemory) denied() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.denials++
	if m.ticking || m.ctx.Err() != nil {
		return
	}
	m.ticking = true
	go m.tick()
}

func (m *localMemory) tick() {
	ticker := time.NewTicker(m.every)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			m.mu.Lock()
			m.ticking = false
			m.mu.Unlock()
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		seen := m.denials
		m.mu.Unlock()
		if !m.queued(job.WaitAdmit) {
			m.mu.Lock()
			if m.denials == seen {
				m.ticking = false
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
		}
		m.recheck()
	}
}

// queued reports a queued job waiting for kind, or for anything when kind
// is empty.
func (m *localMemory) queued(kind job.WaitKind) bool {
	for _, info := range m.jobs.Status() {
		if info.State == job.StateQueued && (kind == "" || info.WaitKind == kind) {
			return true
		}
	}
	return false
}

// recheck refreshes the readings and lets the table reconsider its queue.
func (m *localMemory) recheck() {
	m.mu.Lock()
	hosts := make([]string, 0, len(m.hosts))
	for host := range m.hosts {
		hosts = append(hosts, host)
	}
	m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	m.monitor.Refresh(m.ctx, hosts...)
	m.jobs.Recheck()
}

// jobEndedForMemory refreshes the readings off the caller's goroutine while
// a job is queued, since the one that ended may have freed memory.
func (a *Agency) jobEndedForMemory() {
	m := a.localMemory()
	if m == nil || m.ctx.Err() != nil {
		return
	}
	go func() {
		if m.queued("") {
			m.recheck()
		}
	}()
}
