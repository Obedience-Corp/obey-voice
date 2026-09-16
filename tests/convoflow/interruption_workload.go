package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// Request intervals are host observations around real provider calls, not
// server token timestamps. No prompts, responses or provider errors are stored.
type interruptionWorkloadRequest struct {
	StartedOffset time.Duration `json:"started_offset_ns"`
	EndedOffset   time.Duration `json:"ended_offset_ns"`
	Completed     bool          `json:"completed"`
	Failed        bool          `json:"failed"`
}

type interruptionWorkloadReport struct {
	Condition                string                        `json:"condition"`
	JobID                    string                        `json:"job_id,omitempty"`
	Scope                    work.WorkScope                `json:"scope"`
	Provider                 string                        `json:"provider,omitempty"`
	Model                    string                        `json:"model,omitempty"`
	Host                     string                        `json:"host,omitempty"`
	StartedAt                time.Time                     `json:"started_at"`
	StimulusOffset           time.Duration                 `json:"stimulus_offset_ns"`
	StoppedOffset            time.Duration                 `json:"stopped_offset_ns"`
	RunningAtStimulus        bool                          `json:"running_at_stimulus"`
	RunningAtStop            bool                          `json:"running_at_stop"`
	SuccessfulBeforeStimulus int                           `json:"successful_before_stimulus"`
	Requests                 []interruptionWorkloadRequest `json:"requests,omitempty"`
	StoppedAndJoined         bool                          `json:"stopped_and_joined"`
	Error                    string                        `json:"error,omitempty"`
}

// Shared across Fresh providers, scoped anew before each managed admission.
// Recording is disabled until begin; ordinary provider construction does no I/O.
type workloadEvidence struct {
	mu       sync.Mutex
	origin   time.Time
	scope    work.WorkScope
	jobID    string
	joined   bool
	active   int
	requests []interruptionWorkloadRequest
	overflow bool
}

func (e *workloadEvidence) begin(scope work.WorkScope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != 0 {
		return errors.New("workload evidence: previous provider still active")
	}
	e.origin, e.scope, e.jobID, e.joined = time.Now(), scope, "", false
	e.requests, e.overflow = nil, false
	return nil
}
func (e *workloadEvidence) bind(key work.JobKey) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if key.Scope != e.scope || key.JobID == "" {
		return errors.New("workload evidence: admission scope changed")
	}
	e.jobID = key.JobID
	return nil
}
func (e *workloadEvidence) loopStarted() func() {
	if e == nil {
		return func() {}
	}
	e.mu.Lock()
	e.active++
	e.mu.Unlock()
	return func() { e.mu.Lock(); e.active--; e.mu.Unlock() }
}
func (e *workloadEvidence) requestStarted(at time.Time) int {
	if e == nil {
		return -1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.origin.IsZero() {
		return -1
	}
	if len(e.requests) >= 4096 {
		e.overflow = true
		return -1
	}
	e.requests = append(e.requests, interruptionWorkloadRequest{StartedOffset: at.Sub(e.origin)})
	return len(e.requests) - 1
}
func (e *workloadEvidence) requestEnded(index int, at time.Time, err error) {
	if e == nil || index < 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests[index].EndedOffset = at.Sub(e.origin)
	e.requests[index].Completed = err == nil
	e.requests[index].Failed = err != nil && !errors.Is(err, context.Canceled)
}
func (e *workloadEvidence) snapshot() (interruptionWorkloadReport, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := interruptionWorkloadReport{Condition: "worker", JobID: e.jobID, Scope: e.scope, StartedAt: e.origin, StoppedAndJoined: e.joined, Requests: append([]interruptionWorkloadRequest(nil), e.requests...)}
	if e.overflow {
		r.Error = "provider request evidence overflow"
	}
	return r, e.active
}
func (r *liveRunner) workloadEvidence() *workloadEvidence {
	if r.worker != nil {
		if w, ok := r.worker.inner.(*sustainedLocalWorker); ok {
			return w.evidence
		}
	}
	return nil
}
func (e *workloadEvidence) markJoined(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id != e.jobID || e.active != 0 {
		return errors.New("workload evidence: provider not joined or wrong job")
	}
	e.joined = true
	return nil
}

func validateInterruptionWorkload(r interruptionWorkloadReport, condition string) error {
	if r.Condition != condition || (condition != "baseline" && condition != "worker") {
		return errors.New("interruption workload condition mismatch")
	}
	if r.Error != "" {
		return fmt.Errorf("interruption workload: %s", r.Error)
	}
	if !r.Scope.Valid() || r.StartedAt.IsZero() || r.StimulusOffset <= 0 || r.StoppedOffset <= r.StimulusOffset {
		return errors.New("interruption workload: missing scope or measurement boundaries")
	}
	if condition == "baseline" {
		if r.JobID != "" || len(r.Requests) != 0 || r.RunningAtStimulus || r.RunningAtStop || r.SuccessfulBeforeStimulus != 0 {
			return errors.New("baseline interruption contains worker activity")
		}
		return nil
	}
	if r.JobID == "" || r.Provider != "ollama" || r.Model == "" || r.Host == "" || !r.RunningAtStimulus || !r.RunningAtStop || !r.StoppedAndJoined {
		return errors.New("interruption workload: missing live worker or joined provider evidence")
	}
	before, overlap := 0, false
	var lastEnd time.Duration
	for _, q := range r.Requests {
		if q.StartedOffset < 0 || q.EndedOffset <= q.StartedOffset || q.StartedOffset < lastEnd || q.Failed {
			return errors.New("interruption workload: failed, unfinished or unordered provider request")
		}
		lastEnd = q.EndedOffset
		if q.Completed && q.EndedOffset <= r.StimulusOffset {
			before++
		}
		if q.Completed && q.StartedOffset < r.StoppedOffset && q.EndedOffset > r.StimulusOffset {
			overlap = true
		}
	}
	if before == 0 || before != r.SuccessfulBeforeStimulus || !overlap {
		return errors.New("interruption workload: successful real requests before and across interruption required")
	}
	return nil
}

type interruptionWorkloadProbe struct {
	mu       sync.Mutex
	runner   *liveRunner
	evidence *workloadEvidence
	report   interruptionWorkloadReport
	origin   time.Time
}

func (r *liveRunner) beginInterruptionWorkload(ctx context.Context) (*interruptionWorkloadProbe, error) {
	s := r.workSnapshot()
	p := &interruptionWorkloadProbe{runner: r, evidence: r.workloadEvidence(), origin: time.Now()}
	p.report = interruptionWorkloadReport{Condition: "baseline", Scope: s.Scope, StartedAt: p.origin}
	if p.evidence != nil {
		p.report, _ = p.evidence.snapshot()
		p.origin = p.report.StartedAt
		cfg := config.WorkerBrainConfig(r.cfg)
		p.report.Provider, p.report.Model = cfg.BrainProvider, cfg.OllamaModel
		u, err := url.Parse(cfg.OllamaHost)
		if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return p, errors.New("interruption workload: explicit credential-free worker URL required")
		}
		p.report.Host = strings.TrimRight(u.String(), "/")
		if p.report.JobID == "" || !p.running() {
			return p, errors.New("interruption workload: expected exactly one scoped running job")
		}
		// Canonical running is not proof that the server handled a request.
		readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			snap, _ := p.evidence.snapshot()
			for _, q := range snap.Requests {
				if q.Completed {
					return p, nil
				}
				if q.Failed {
					return p, errors.New("interruption workload: provider request failed before stimulus")
				}
			}
			if !p.running() {
				return p, errors.New("interruption workload: worker ended before provider readiness")
			}
			select {
			case <-readyCtx.Done():
				return p, fmt.Errorf("interruption workload readiness: %w", readyCtx.Err())
			case <-ticker.C:
			}
		}
	}
	if !p.cleanBaseline() {
		return p, errors.New("baseline interruption has pending or active work")
	}
	return p, nil
}
func (p *interruptionWorkloadProbe) cleanBaseline() bool {
	s := p.runner.workSnapshot()
	if s.Scope != p.report.Scope || len(s.Pending) > 0 {
		return false
	}
	for _, j := range s.Jobs {
		if j.State != work.JobTerminal {
			return false
		}
	}
	return true
}
func (p *interruptionWorkloadProbe) running() bool {
	s := p.runner.workSnapshot()
	if s.Scope != p.report.Scope || len(s.Pending) > 0 {
		return false
	}
	count := 0
	for _, j := range s.Jobs {
		if j.State != work.JobTerminal {
			if j.State != work.JobRunning || j.Key.JobID != p.report.JobID || j.Key.Scope != p.report.Scope {
				return false
			}
			count++
		}
	}
	return count == 1
}
func (p *interruptionWorkloadProbe) observe(at time.Time, stopped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	running := p.running()
	if p.report.Condition == "baseline" && !p.cleanBaseline() {
		p.report.Error = "baseline acquired worker activity"
	}
	if stopped {
		p.report.StoppedOffset = at.Sub(p.origin)
		p.report.RunningAtStop = running
	} else {
		p.report.StimulusOffset = at.Sub(p.origin)
		p.report.RunningAtStimulus = running
	}
}
func (p *interruptionWorkloadProbe) finish(success bool) (interruptionWorkloadReport, error) {
	p.mu.Lock()
	r := p.report
	p.mu.Unlock()
	var cleanupErr error
	if p.evidence != nil && r.JobID != "" {
		// Successful completion of an overlapping request proves this was actual
		// provider work, not a permanently stalled HTTP call. Waiting is outside the
		// recorded interruption boundary and never affects detector-to-stop timing.
		if success {
			waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			ticker := time.NewTicker(5 * time.Millisecond)
		waiting:
			for {
				snap, _ := p.evidence.snapshot()
				for _, q := range snap.Requests {
					if q.Completed && q.StartedOffset < r.StoppedOffset && q.EndedOffset > r.StimulusOffset {
						break waiting
					}
					if q.Failed {
						break waiting
					}
				}
				select {
				case <-waitCtx.Done():
					break waiting
				case <-ticker.C:
				}
			}
			ticker.Stop()
			cancel()
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr = p.runner.stopLatencyWorker(stopCtx, r.JobID)
		cancel()
		snap, _ := p.evidence.snapshot()
		r.Requests, r.StoppedAndJoined = snap.Requests, snap.StoppedAndJoined
		if snap.Error != "" {
			r.Error = snap.Error
		}
		for _, q := range r.Requests {
			if q.Completed && q.EndedOffset <= r.StimulusOffset {
				r.SuccessfulBeforeStimulus++
			}
		}
	}
	if cleanupErr != nil {
		r.Error = "managed worker cleanup failed"
	}
	if success {
		cleanupErr = errors.Join(cleanupErr, validateInterruptionWorkload(r, r.Condition))
	}
	return r, cleanupErr
}
