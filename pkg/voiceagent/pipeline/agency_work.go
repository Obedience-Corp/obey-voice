package pipeline

import (
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func newWorkID() string {
	return "work-" + rand.Text()
}

func providerName(cfg *config.Config) string { return config.VoiceWorkerProviderOf(cfg) }
func modelName(cfg *config.Config) string {
	return config.BrainModelFor(config.WorkerBrainConfig(cfg), providerName(cfg))
}
func frozenConfig(cfg *config.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	return map[string]string{"provider": providerName(cfg), "model": modelName(cfg), "tools": fmt.Sprint(cfg.VoiceToolsEnabled), "persona": cfg.ActivePersona, "work_dir": cfg.WorkDir}
}
func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	c := *cfg
	c.SkillsDisabled = append([]string(nil), cfg.SkillsDisabled...)
	return &c
}

func (a *Agency) workAuthority() (*work.Coordinator, work.WorkScope) {
	a.workMu.RLock()
	defer a.workMu.RUnlock()
	return a.coordinator, a.workScope
}
func (a *Agency) workEnabled() bool { c, _ := a.workAuthority(); return c != nil }
func (a *Agency) callbackKey(id string) (*work.Coordinator, work.JobKey, bool) {
	a.workMu.RLock()
	c, scope := a.coordinator, a.workScope
	jobScope, exists := a.jobScopes[id]
	a.workMu.RUnlock()
	if c == nil {
		return nil, work.JobKey{}, true
	}
	return c, work.JobKey{Scope: jobScope, JobID: id}, exists && scope == jobScope && !c.Snapshot(scope).Retired
}
func (a *Agency) acceptJobCallback(id string) bool   { _, _, ok := a.callbackKey(id); return ok }
func (a *Agency) WorkCoordinator() *work.Coordinator { c, _ := a.workAuthority(); return c }
func (a *Agency) WorkSnapshot() work.Snapshot {
	c, scope := a.workAuthority()
	if c == nil {
		return work.Snapshot{}
	}
	return c.Snapshot(scope)
}
func (a *Agency) SetWorkObserver(observer func(work.Snapshot)) {
	a.workMu.Lock()
	a.workObserver = observer
	a.workMu.Unlock()
}

// Lifecycle methods run at the host's serialized turn boundary. Worker
// callbacks carry their admission scope and never read the selected brain.
func (a *Agency) EnableConversationWork(sessionID string, saved *work.Snapshot, cfg *config.Config, observer func(work.Snapshot)) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}
	if current := a.WorkSnapshot(); current.Scope.Valid() && !current.Retired {
		return errors.New("retire the current conversation before enabling a new work scope")
	}
	scope := work.WorkScope{SessionID: sessionID, RuntimeEpoch: newWorkID(), ConversationEpoch: newWorkID()}
	initial := scope
	if saved != nil {
		initial = saved.Scope
	}
	c := work.NewCoordinator(initial, work.WithChangeCallback(a.workChanged))
	if saved != nil {
		if err := c.Restore(*saved, scope); err != nil {
			return err
		}
	}
	if err := a.attachManaged(a.pipeline.Brain, c, scope, cfg); err != nil {
		return err
	}
	// Explicit new-session/persona transitions may reuse the Agency object.
	// Select its future worker from the new configuration before publishing.
	if !reflect.DeepEqual(a.currentConfig(), cfg) {
		var worker brain.Provider
		var workerErr error
		if providerName(cfg) != "ollama" {
			workerErr = errors.New("experimental work requires a local worker")
		} else if cfg != nil {
			worker, workerErr = brain.NewWorkerProvider(cfg)
		}
		a.ReplaceWorker(worker, cfg, workerErr)
	}
	a.mu.Lock()
	a.cfg = cloneConfig(cfg)
	a.pending = nil
	a.mu.Unlock()
	a.workMu.Lock()
	a.coordinator, a.workScope, a.workObserver = c, scope, observer
	a.jobScopes = make(map[string]work.WorkScope)
	a.workMu.Unlock()
	a.workChanged(c.Snapshot(scope))
	return nil
}

func (a *Agency) workChanged(snapshot work.Snapshot) {
	a.workMu.RLock()
	c, scope, observer := a.coordinator, a.workScope, a.workObserver
	a.workMu.RUnlock()
	if c == nil || scope != snapshot.Scope {
		return
	}
	// Dispatch only copied snapshots and never hold authority locks across
	// display, disk, or reentrant host callbacks.
	a.pipeline.emitWorkSnapshot(snapshot)
	if observer != nil {
		observer(snapshot)
	}
}

func (a *Agency) ClearConversation() error {
	c, old := a.workAuthority()
	if c == nil {
		return nil
	}
	if err := c.Retire(old); err != nil && !errors.Is(err, work.ErrRetired) {
		return err
	}
	next := work.WorkScope{SessionID: old.SessionID, RuntimeEpoch: old.RuntimeEpoch, ConversationEpoch: newWorkID()}
	a.workMu.Lock()
	a.workScope = next
	a.jobScopes = make(map[string]work.WorkScope)
	a.workMu.Unlock()
	if err := c.Reset(next); err != nil {
		return err
	}
	a.abortAll()
	closeErr := a.closeManaged()
	attachErr := a.attachManaged(a.pipeline.Brain, c, next, a.currentConfig())
	return errors.Join(closeErr, attachErr)
}

func (a *Agency) abortAll() {
	var queued, running []string
	for _, info := range a.jobs.Status() {
		switch info.State {
		case job.StateQueued:
			queued = append(queued, info.ID)
		case job.StateRunning:
			running = append(running, info.ID)
		}
	}
	for _, id := range append(queued, running...) {
		a.jobs.Abort(id)
	}
}

func (a *Agency) CloseWork() error {
	c, scope := a.workAuthority()
	if c == nil {
		return nil
	}
	a.workMu.Lock()
	a.jobScopes = make(map[string]work.WorkScope)
	a.workMu.Unlock()
	if err := c.Retire(scope); err != nil && !errors.Is(err, work.ErrRetired) {
		return err
	}
	a.abortAll()
	return a.closeManaged()
}

func (a *Agency) closeManaged() error {
	a.workMu.Lock()
	runtimes := make([]*delegation.Runtime, 0, len(a.managed))
	for runtime := range a.managed {
		runtimes = append(runtimes, runtime)
	}
	a.managed = make(map[*delegation.Runtime]struct{})
	a.managedJobs = make(map[string]*delegation.Runtime)
	a.managedCurrent = nil
	a.workMu.Unlock()
	results := make(chan error, len(runtimes))
	for _, runtime := range runtimes {
		runtime.Stop()
		go func() { results <- runtime.Close() }()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var errs []error
	for range runtimes {
		select {
		case err := <-results:
			errs = append(errs, err)
		case <-deadline.C:
			return errors.Join(append(errs, errors.New("managed runtime close timed out; termination unconfirmed"))...)
		}
	}
	return errors.Join(errs...)
}

func (a *Agency) ReplaceWorker(worker brain.Provider, cfg *config.Config, workerErr error) {
	a.mu.Lock()
	a.worker, a.cfg, a.workerInitErr = worker, cloneConfig(cfg), workerErr
	a.mu.Unlock()
}

// attachManaged borrows this provider's own runtime. A retained old runtime
// keeps its original observer/config; the selected provider never inherits it.
// Closed runtimes rotate on clear/restore so future jobs remain available.
func (a *Agency) attachManaged(provider brain.Provider, c *work.Coordinator, scope work.WorkScope, cfg *config.Config) error {
	binding, ok := provider.(brain.DelegationBinding)
	if !ok {
		a.selectManaged(nil)
		return nil
	}
	runtime := binding.DetachDelegation()
	if runtime == nil {
		a.selectManaged(nil)
		return nil
	}
	if !runtime.Enabled() {
		runtime = runtime.NewSession()
	}
	if runtime.Task() != "" {
		_ = binding.AttachDelegation(runtime)
		return errors.New("cannot change ownership of an already admitted managed task")
	}
	if err := binding.AttachDelegation(runtime); err != nil {
		return errors.Join(err, runtime.Close())
	}
	observer := a.managedObserver(c, scope, runtime, cfg)
	if err := runtime.AttachConversation(observer); err != nil {
		_ = binding.DetachDelegation()
		return errors.Join(err, runtime.Close())
	}
	a.workMu.Lock()
	a.managed[runtime] = struct{}{}
	a.workMu.Unlock()
	a.selectManaged(runtime)
	return nil
}

// prepareManaged attaches the candidate runtime and its host observer without
// publishing it in the Agency registry. The caller owns cleanup until commit.
func (a *Agency) prepareManaged(provider brain.Provider, c *work.Coordinator, scope work.WorkScope, cfg *config.Config, prepared *PreparedBrainReplacement) error {
	binding, ok := provider.(brain.DelegationBinding)
	if !ok {
		return nil
	}
	runtime := binding.DetachDelegation()
	if runtime == nil {
		return nil
	}
	// A stopped candidate cannot safely be revived as part of a selection
	// transaction. Clear/restore retains their explicit legacy rotation path;
	// responder selection must leave the old owner untouched on this failure.
	if !runtime.Enabled() {
		_ = runtime.Close()
		return errors.New("replacement managed runtime is closed")
	}
	if runtime.Task() != "" {
		_ = binding.AttachDelegation(runtime)
		return errors.New("cannot change ownership of an already admitted managed task")
	}
	if err := binding.AttachDelegation(runtime); err != nil {
		return errors.Join(err, runtime.Close())
	}
	prepared.binding = binding
	if err := runtime.AttachConversation(a.managedObserver(c, scope, runtime, cfg)); err != nil {
		_ = binding.DetachDelegation()
		return errors.Join(err, runtime.Close())
	}
	prepared.runtime = runtime
	return nil
}

func (a *Agency) managedObserver(c *work.Coordinator, scope work.WorkScope, runtime *delegation.Runtime, cfg *config.Config) delegation.ConversationObserver {
	metadata := map[string]string{}
	if cfg != nil {
		metadata = map[string]string{"profile": cfg.DelegationProfile, "summarizer_provider": cfg.BrainProvider, "summarizer_model": config.BrainModelFor(cfg, cfg.BrainProvider), "persona": cfg.ActivePersona}
	}
	return delegation.ConversationObserver{
		Admitted: func(ticket, task string) error {
			_, err := c.Register(scope, work.Admission{JobID: ticket, Task: task, Origin: "managed", Provider: "obey", Config: metadata})
			if err == nil {
				a.workMu.Lock()
				if a.workScope == scope {
					a.managedJobs[ticket] = runtime
				}
				a.workMu.Unlock()
			}
			return err
		},
		Started: func(ticket string) { _ = c.Started(work.JobKey{Scope: scope, JobID: ticket}) },
		Finished: func(out delegation.TerminalOutcome) error {
			status := work.OutcomeInterrupted
			termination := out.Termination
			if termination == "" {
				termination = "unknown"
			}
			switch strings.ToLower(strings.TrimSpace(out.State)) {
			case "completed", "succeeded", "done":
				status = work.OutcomeSucceeded
			case "partial":
				status = work.OutcomePartial
			case "failed", "rejected":
				status = work.OutcomeFailed
			case "canceled", "cancelled", "aborted":
				status = work.OutcomeCancelled
			case "timed_out", "timeout":
				status = work.OutcomeTimedOut
			}
			text := strings.TrimSpace(out.Brief)
			if text == "" {
				text = strings.TrimSpace(out.Err)
			}
			if text == "" {
				text = "Work ended without a confirmed result."
			}
			_, err := c.Publish(work.JobKey{Scope: scope, JobID: out.Ticket}, work.Outcome{
				Scope: scope, JobID: out.Ticket, Status: status, Text: text, Limitations: out.Err,
				SourceRef: out.SourceRef, StartedAt: out.Started, EndedAt: out.Ended, Elapsed: out.Elapsed, Termination: termination,
			})
			a.workMu.Lock()
			delete(a.managedJobs, out.Ticket)
			a.workMu.Unlock()
			a.retireManagedIfUnused(runtime, true)
			return err
		},
	}
}

func (a *Agency) selectManaged(current *delegation.Runtime) {
	a.workMu.Lock()
	a.managedCurrent = current
	var previous []*delegation.Runtime
	for runtime := range a.managed {
		if runtime != current {
			previous = append(previous, runtime)
		}
	}
	a.workMu.Unlock()
	for _, runtime := range previous {
		a.retireManagedIfUnused(runtime, false)
	}
}

func (a *Agency) retireManagedIfUnused(runtime *delegation.Runtime, terminal bool) {
	if !terminal && runtime.Task() != "" {
		return
	}
	a.workMu.Lock()
	if runtime == a.managedCurrent || a.managedClosing[runtime] {
		a.workMu.Unlock()
		return
	}
	if _, owned := a.managed[runtime]; !owned {
		a.workMu.Unlock()
		return
	}
	a.managedClosing[runtime] = true
	a.workMu.Unlock()
	// A terminal callback still belongs to runtime's worker goroutine. Close
	// joins it asynchronously; keep the registry entry until the join ends so
	// final shutdown can also account for it.
	runtime.Stop()
	go func() {
		_ = runtime.Close()
		a.workMu.Lock()
		delete(a.managed, runtime)
		delete(a.managedClosing, runtime)
		a.workMu.Unlock()
	}()
}

// CancelWork addresses the original owner even after its brain was replaced.
// It records cancellation intent; only the worker's terminal observation can
// claim confirmed execution termination.
func (a *Agency) CancelWork(id string) error {
	a.workMu.RLock()
	runtime := a.managedJobs[id]
	a.workMu.RUnlock()
	if runtime != nil {
		return runtime.Cancel(id)
	}
	for _, item := range a.jobs.Status() {
		if item.ID == id {
			a.jobs.Abort(id)
			return nil
		}
	}
	return work.ErrUnknown
}
