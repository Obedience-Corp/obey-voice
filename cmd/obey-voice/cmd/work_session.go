//go:build !integration

package cmd

import (
	"errors"

	"github.com/Obedience-Corp/obey-voice/internal/worksession"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// workCheckpoint preserves the command-local API while sharing the same
// coalescing adapter with the conversation harness.
type workCheckpoint struct{ inner *worksession.Checkpoint }

func bindWorkSession(p *pipeline.Pipeline, cfg *config.Config, sess *session.Session, store *session.Store) (*workCheckpoint, error) {
	if cfg != nil && sess != nil && cfg.WorkDirSource != "" {
		sess.WorkDir, sess.WorkDirSource = cfg.WorkDir, cfg.WorkDirSource
	}
	inner, err := worksession.Bind(p, cfg, sess, store)
	if err != nil || inner == nil {
		return nil, err
	}
	return &workCheckpoint{inner: inner}, nil
}

func (w *workCheckpoint) submit(snapshot work.Snapshot) {
	if w != nil && w.inner != nil {
		w.inner.Submit(snapshot)
	}
}

func (w *workCheckpoint) retry() {
	if w != nil && w.inner != nil {
		w.inner.Retry()
	}
}

func (w *workCheckpoint) Close() error {
	if w == nil || w.inner == nil {
		return nil
	}
	return w.inner.Close()
}

func closeWorkSession(p *pipeline.Pipeline, checkpoint *workCheckpoint) error {
	return errors.Join(p.CloseWork(), checkpoint.Close())
}
