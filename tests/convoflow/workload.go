package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// startLatencyWorker admits one real Agency job without asking the live brain
// to classify a spawn phrase. This keeps the workload trigger deterministic
// while the job itself still uses the configured local Ollama worker provider.
// The caller must stop it after the paired turns.
func (r *liveRunner) startLatencyWorker(ctx context.Context, task string) (id string, runErr error) {
	if r == nil || r.agent == nil || r.agent.Pipeline == nil || r.agent.Pipeline.Agency == nil {
		return "", fmt.Errorf("latency worker: Agency is not configured")
	}
	if r.cfg == nil {
		return "", fmt.Errorf("latency worker: requires a local Ollama worker")
	}
	workerProvider := strings.TrimSpace(r.cfg.VoiceWorkerProvider)
	if workerProvider == "" {
		workerProvider = r.cfg.BrainProvider
	}
	if !strings.EqualFold(workerProvider, "ollama") {
		return "", fmt.Errorf("latency worker: requires a local Ollama worker")
	}
	if r.cfg.VoiceWorkers != nil && r.cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		return "", fmt.Errorf("latency worker: tiered worker assignment is not deterministic")
	}
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("latency worker: task is empty")
	}
	before := r.workSnapshot()
	known := make(map[string]struct{}, len(before.Jobs))
	for _, j := range before.Jobs {
		known[j.Key.JobID] = struct{}{}
	}
	evidence := r.workloadEvidence()
	if evidence != nil {
		if err := evidence.begin(before.Scope); err != nil {
			return "", err
		}
	}
	defer func() {
		if runErr == nil {
			return
		}
		// Admission can succeed even when readiness fails or its context expires.
		// Cancel only jobs admitted by this call, under the original scope.
		for _, j := range r.workSnapshot().Jobs {
			if _, exists := known[j.Key.JobID]; exists || j.Key.Scope != before.Scope {
				continue
			}
			if evidence != nil {
				runErr = errors.Join(runErr, evidence.bind(j.Key))
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			runErr = errors.Join(runErr, r.stopLatencyWorker(cleanupCtx, j.Key.JobID))
			cancel()
		}
	}()
	r.agent.Pipeline.Agency.HandleRoute(ctx, nil, nil, pipeline.RouteDecision{
		Kind: pipeline.RouteSpawn, Transcript: task, Source: "convoflow-latency",
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, j := range r.workSnapshot().Jobs {
			if j.State == work.JobRunning {
				if _, already := known[j.Key.JobID]; already {
					continue
				}
				if evidence != nil {
					if err := evidence.bind(j.Key); err != nil {
						return "", err
					}
				}
				return j.Key.JobID, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("latency worker: Agency did not retain a running local job")
		case <-ticker.C:
		}
	}
}

func (r *liveRunner) stopLatencyWorker(ctx context.Context, id string) error {
	if r == nil || r.agent == nil || r.agent.Pipeline == nil {
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("latency worker: job id is empty")
	}
	evidence := r.workloadEvidence()
	if evidence != nil {
		saved, _ := evidence.snapshot()
		if saved.JobID != id || saved.Scope != r.workSnapshot().Scope {
			return fmt.Errorf("latency worker: cleanup scope/job mismatch")
		}
		if saved.StoppedAndJoined {
			return nil
		}
	}
	terminalAlready := false
	for _, j := range r.workSnapshot().Jobs {
		if j.Key.JobID == id {
			terminalAlready = j.State == work.JobTerminal
		}
	}
	if !terminalAlready {
		if err := r.agent.Pipeline.CancelWork(id); err != nil {
			return fmt.Errorf("latency worker %s cancel: %w", id, err)
		}
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		s := r.workSnapshot()
		terminal := false
		for _, j := range s.Jobs {
			if j.Key.JobID == id {
				terminal = j.State == work.JobTerminal
				break
			}
		}
		if terminal {
			if evidence != nil {
				_, active := evidence.snapshot()
				if active != 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ticker.C:
					}
					continue
				}
			}
			// Remove only this workload's terminal result. Other results remain
			// available for the next measurement or host delivery.
			for _, v := range s.Pending {
				if v.Outcome.JobID == id {
					if err := r.agent.Pipeline.WorkResultAction(s.Scope, v.Outcome.ResultID, "dismiss"); err != nil {
						return fmt.Errorf("latency worker %s dismiss: %w", id, err)
					}
				}
			}
			if evidence != nil {
				return evidence.markJoined(id)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
