package netapi

import (
	"net/http"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// WorkStatus is a limited projection of the host's work state. In particular,
// provider configuration, output text, and source paths stay out of this view.
type WorkStatus struct {
	Scope    work.WorkScope  `json:"scope"`
	Revision uint64          `json:"state_revision"`
	Jobs     []WorkStatusJob `json:"jobs"`
}

type WorkStatusJob struct {
	Key      work.JobKey        `json:"key"`
	Task     string             `json:"task"`
	Provider string             `json:"provider"`
	Model    string             `json:"model"`
	State    work.JobState      `json:"state"`
	Outcome  *WorkStatusOutcome `json:"outcome,omitempty"`
}

type WorkStatusOutcome struct {
	Status work.OutcomeStatus `json:"status"`
}

func (s *Server) handleCurrentWork(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.opts.CurrentWorkSnapshot()
	if !snapshot.Scope.Valid() || snapshot.Retired {
		writeJSON(w, http.StatusOK, map[string]any{"work": nil})
		return
	}
	result := WorkStatus{Scope: snapshot.Scope, Revision: snapshot.Revision, Jobs: make([]WorkStatusJob, 0, len(snapshot.Jobs))}
	for _, job := range snapshot.Jobs {
		item := WorkStatusJob{Key: job.Key, Task: job.Task, Provider: job.Provider, Model: job.Model, State: job.State}
		if job.Outcome != nil {
			item.Outcome = &WorkStatusOutcome{Status: job.Outcome.Status}
		}
		result.Jobs = append(result.Jobs, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"work": result})
}
