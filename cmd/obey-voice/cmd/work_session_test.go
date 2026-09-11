//go:build !integration

package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/transcript"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type checkpointBrain struct{ servePersonaTestBrain }

func (b *checkpointBrain) ClearHistory() { b.history = nil }

func checkpointPipeline(t *testing.T) (*pipeline.Pipeline, *config.Config) {
	t.Helper()
	cfg := &config.Config{VoiceWorkEnabled: true, BrainProvider: "ollama", OllamaModel: "test", OllamaHost: "http://127.0.0.1:1"}
	b := &checkpointBrain{}
	p := &pipeline.Pipeline{Brain: b, Events: events.NewBus()}
	table := job.NewTable(t.Context())
	pipeline.NewAgency(p, table, b, cfg, nil).Wire()
	t.Cleanup(table.Close)
	p.SetOutputMuted(true)
	return p, cfg
}

func publishCheckpointResult(t *testing.T, p *pipeline.Pipeline, text string) work.ResultView {
	t.Helper()
	c := p.WorkCoordinator()
	key, err := c.Register(p.WorkSnapshot().Scope, work.Admission{Task: "Inspect local files", Origin: "agency", Provider: "ollama", Model: "original-model"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	result, err := c.Publish(key, work.Outcome{Scope: key.Scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: text, StartedAt: started, EndedAt: started.Add(2 * time.Second), Elapsed: 2 * time.Second, Termination: "known"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func waitCheckpoint(t *testing.T, p *pipeline.Pipeline) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s := p.WorkSnapshot()
		if s.PersistedRevision == s.Revision {
			return
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatalf("checkpoint remained dirty: %+v", p.WorkSnapshot())
		}
	}
}

func TestWorkCheckpointMutePersistsAndRestoresHeldWithoutSpeech(t *testing.T) {
	p, cfg := checkpointPipeline(t)
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "test")
	path := filepath.Join(t.TempDir(), "work.jsonl")
	writer, err := transcript.NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	writer.Attach(p.Events)
	var speech atomic.Int32
	events.Subscribe(p.Events, func(events.SpeakingStarted) { speech.Add(1) })
	cp, err := bindWorkSession(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	result := publishCheckpointResult(t, p, "Checked three files; all match.")
	waitCheckpoint(t, p)
	if err := closeWorkSession(p, cp); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Work == nil || len(saved.Work.Pending) != 1 {
		t.Fatalf("saved inbox = %+v", saved.Work)
	}
	durable := saved.Work.Pending[0]
	if durable.Outcome.Text != result.Outcome.Text || durable.Outcome.Elapsed != 2*time.Second || durable.State != work.Pending {
		t.Fatalf("durable outcome = %+v", durable)
	}
	if saved.Work.PersistedRevision != saved.Work.Revision {
		t.Fatal("saved revision lacks durable watermark")
	}
	// The same canonical identity and text are exposed through diagnostics.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	found := false
	for scanner.Scan() {
		var record struct {
			Type string         `json:"type"`
			Work *work.Snapshot `json:"work"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == "work_state_changed" && record.Work != nil && len(record.Work.Pending) == 1 {
			got := record.Work.Pending[0].Outcome
			if got.ResultID != result.Outcome.ResultID || got.Text != result.Outcome.Text || got.Elapsed != result.Outcome.Elapsed {
				t.Fatalf("diagnostic disagrees: %+v", got)
			}
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no canonical public work event")
	}
	resumed, _ := checkpointPipeline(t)
	events.Subscribe(resumed.Events, func(events.SpeakingStarted) { speech.Add(1) })
	resumedCP, err := bindWorkSession(resumed, cfg, saved, store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeWorkSession(resumed, resumedCP); err != nil {
			t.Error(err)
		}
	}()
	snapshot := resumed.WorkSnapshot()
	if snapshot.Scope == result.Outcome.Scope || len(snapshot.Pending) != 1 {
		t.Fatalf("restored state = %+v", snapshot)
	}
	held := snapshot.Pending[0]
	if held.State != work.Held || !held.Outcome.PreviousSession || held.Outcome.Scope != result.Outcome.Scope || held.DeliveryKey.Scope != snapshot.Scope {
		t.Fatalf("restored result = %+v", held)
	}
	if speech.Load() != 0 || resumed.JobActive() {
		t.Fatal("restore spoke or restarted work")
	}
	if _, err := resumed.WorkCoordinator().Publish(result.DeliveryKey, result.Outcome); !errors.Is(err, work.ErrStaleScope) {
		t.Fatalf("late prior-runtime publication = %v", err)
	}
}

func TestWorkCheckpointFailureStaysDirtyAndRetries(t *testing.T) {
	p, cfg := checkpointPipeline(t)
	dir := filepath.Join(t.TempDir(), "sessions")
	store := session.NewStore(dir)
	sess := session.New("ollama", "test")
	cp, err := bindWorkSession(p, cfg, sess, store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeWorkSession(p, cp) }()
	failures := make(chan events.Error, 1)
	events.Subscribe(p.Events, func(e events.Error) {
		if e.Stage == "work_checkpoint" {
			select {
			case failures <- e:
			default:
			}
		}
	})
	if err := os.Rename(dir, dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	publishCheckpointResult(t, p, "Retain me if storage fails.")
	select {
	case <-failures:
	case <-time.After(3 * time.Second):
		t.Fatal("checkpoint failure was not surfaced")
	}
	if s := p.WorkSnapshot(); s.PersistedRevision >= s.Revision || len(s.Pending) != 1 {
		t.Fatalf("failed save acknowledged or lost result: %+v", s)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".saved", dir); err != nil {
		t.Fatal(err)
	}
	cp.retry()
	waitCheckpoint(t, p)
	if err := closeWorkSession(p, cp); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Work == nil || len(saved.Work.Pending) != 1 || saved.Work.Pending[0].Outcome.Text != "Retain me if storage fails." {
		t.Fatalf("recovered checkpoint = %+v", saved.Work)
	}
}

func TestWorkCheckpointClearRejectsLateSaveAndPersistsHistory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	p, cfg := checkpointPipeline(t)
	ref := &sessionRef{sess: session.New("ollama", "test")}
	if err := ref.bindWork(p, cfg, ref.sess); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.closeWork(p) }()
	p.Brain.LoadHistory([]brain.Turn{{Role: "user", Content: "Old conversation"}})
	if err := ref.save(p.Brain.History()); err != nil {
		t.Fatal(err)
	}
	publishCheckpointResult(t, p, "Old result")
	old := p.WorkSnapshot()
	if err := p.ClearConversation(); err != nil {
		t.Fatal(err)
	}
	if err := ref.save(p.Brain.History()); err != nil {
		t.Fatal(err)
	}
	ref.checkpoint.submit(old)
	if err := ref.closeWork(p); err != nil {
		t.Fatal(err)
	}
	saved, err := session.Load(ref.id())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) != 0 || len(saved.Work.Pending) != 0 || saved.Work.Scope == old.Scope {
		t.Fatalf("clear resurrected old state: %+v", saved)
	}
}
