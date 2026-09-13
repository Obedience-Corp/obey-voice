package worksession

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/workjournal"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestBindAttachesJournalAndPreservesCanonicalWork(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "model")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	p := &pipeline.Pipeline{Events: events.NewBus()}
	cfg := &config.Config{VoiceWorkEnabled: true, VoiceWorkerProvider: "ollama"}
	a := pipeline.NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	cp, err := Bind(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	if cp == nil || a.WorkCoordinator() == nil {
		t.Fatal("Bind did not establish work checkpoint/coordinator")
	}
	p.Events.Emit(events.JobStarted{ID: "missing", Task: "ignored"})
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := store.WorkDir(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	report, err := workjournal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Records) == 0 {
		t.Fatal("journal sidecar has no records")
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Work == nil || loaded.Work.Scope.SessionID != sess.ID {
		t.Fatalf("canonical work snapshot = %+v", loaded.Work)
	}
}

func TestCheckpointCanonicalSaveSurvivesJournalDirectoryFailure(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "model")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ := store.WorkDir(sess.ID)
	if err := os.WriteFile(dir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	p := &pipeline.Pipeline{Events: events.NewBus()}
	cfg := &config.Config{VoiceWorkEnabled: true, VoiceWorkerProvider: "ollama"}
	a := pipeline.NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	cp, err := Bind(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	c := a.WorkCoordinator()
	scope := c.Scope()
	key, err := c.Register(scope, work.Admission{JobID: "job", Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(key, work.Outcome{Scope: scope, JobID: "job", ResultID: "result", Status: work.OutcomeSucceeded, Text: "done", EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Wait(context.Background()); err != nil {
		t.Fatal("canonical checkpoint failed:", err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Work == nil || len(loaded.Work.Pending) != 1 {
		t.Fatalf("canonical pending = %+v", loaded.Work)
	}
	deadline := time.Now().Add(time.Second)
	for !p.JournalHealth().Incomplete && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.JournalHealth().Incomplete {
		t.Fatal("journal failure was not reported")
	}
	if err := cp.Close(); err == nil {
		t.Fatal("Close hid diagnostic journal failure")
	}
}

func TestCheckpointClearKeepsJournalSequenceAcrossConversationEpoch(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "model")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(context.Background())
	defer jobs.Close()
	p := &pipeline.Pipeline{Events: events.NewBus()}
	cfg := &config.Config{VoiceWorkEnabled: true, VoiceWorkerProvider: "ollama"}
	a := pipeline.NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	cp, err := Bind(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	c := a.WorkCoordinator()
	old := c.Scope()
	k, _ := c.Register(old, work.Admission{JobID: "before", Task: "before"})
	_, _ = c.Publish(k, work.Outcome{Scope: old, JobID: "before", ResultID: "r1", Status: work.OutcomeSucceeded, Text: "before"})
	if err := a.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	now := a.WorkSnapshot()
	k, err = c.Register(now.Scope, work.Admission{JobID: "after", Task: "after"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Publish(k, work.Outcome{Scope: now.Scope, JobID: "after", ResultID: "r2", Status: work.OutcomeSucceeded, Text: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	dir, _ := store.WorkDir(sess.ID)
	rep, err := workjournal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Records) < 3 {
		t.Fatalf("records=%d", len(rep.Records))
	}
	for i := 1; i < len(rep.Records); i++ {
		if rep.Records[i].Seq <= rep.Records[i-1].Seq {
			t.Fatalf("sequence not increasing at %d", i)
		}
	}
}
