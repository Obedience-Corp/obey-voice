package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type deadlineWorker struct {
	deadline chan time.Time
}

func (*deadlineWorker) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("unused")
}
func (w *deadlineWorker) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	deadline, _ := brain.WorkerDeadline(ctx)
	w.deadline <- deadline
	return "done", nil
}
func (*deadlineWorker) ClearHistory()            {}
func (*deadlineWorker) History() []brain.Turn    { return nil }
func (*deadlineWorker) LoadHistory([]brain.Turn) {}

func TestWorkerSeesTheJobBudgetAsItsDeadline(t *testing.T) {
	worker := &deadlineWorker{deadline: make(chan time.Time, 1)}
	jobs := job.NewTable(t.Context(), job.WithBudget(10*time.Minute))
	defer jobs.Close()
	cfg := &config.Config{WorkDir: t.TempDir()}
	a := NewAgency(&Pipeline{}, jobs, worker, cfg, nil)

	before := time.Now()
	if _, err := jobs.Start(t.Context(), job.Spec{Task: "read", Run: a.workerRun("read the files", worker, cfg)}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-worker.deadline:
		if got.Before(before.Add(10*time.Minute)) || got.After(time.Now().Add(10*time.Minute)) {
			t.Fatalf("worker deadline=%v, want the job budget end", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker never ran")
	}
}
