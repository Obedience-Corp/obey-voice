package worksession

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestLegacySessionSaveKeepsDiagnosticSidecar(t *testing.T) {
	dir := t.TempDir()
	store := session.NewStore(dir)
	id := "legacy-compat"
	legacy := []byte(`{"id":"legacy-compat","turns":[{"role":"user","content":"old turn"}]}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	sidecar, err := store.WorkDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sidecar, 0700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("persisted diagnostic evidence\n")
	if err := os.WriteFile(filepath.Join(sidecar, "events-runtime-000001.jsonl"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Turns) != 1 || s.Turns[0].Content != "old turn" {
		t.Fatalf("legacy load=%+v", s.Turns)
	}
	if err := store.Save(s, s.Turns); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(sidecar, "events-runtime-000001.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, marker) {
		t.Fatalf("legacy save changed sidecar: %q", after)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Turns) != 1 || loaded.Turns[0].Content != "old turn" {
		t.Fatalf("saved legacy session=%+v", loaded.Turns)
	}
}

func TestDisabledWorkBindPreservesCanonicalPendingAndSidecar(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess := session.New("ollama", "fixture")
	if err := store.Save(sess, nil); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: sess.ID, RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}
	c := work.NewCoordinator(scope)
	key, err := c.Register(scope, work.Admission{JobID: "job-1", Task: "weather"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Publish(key, work.Outcome{ResultID: "result-1", Scope: scope, JobID: key.JobID, Status: work.OutcomeSucceeded, Text: "sunny"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.HoldWithReason(scope, result.Outcome.ResultID, "muted"); err != nil {
		t.Fatal(err)
	}
	snapshot := c.Snapshot(scope)
	if err := store.BeginWork(sess, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	dir, err := store.WorkDir(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	evidence := []byte("sidecar remains\n")
	if err := os.WriteFile(filepath.Join(dir, "retained.txt"), evidence, 0600); err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Events: events.NewBus()}
	if cp, err := Bind(p, &config.Config{VoiceWorkEnabled: false}, sess, store); err != nil {
		t.Fatal(err)
	} else if cp != nil {
		t.Fatal("disabled work unexpectedly created checkpoint")
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Work == nil || len(loaded.Work.Pending) != 1 || loaded.Work.Pending[0].Outcome.ResultID != "result-1" {
		t.Fatalf("disabled bind lost canonical pending: %+v", loaded.Work)
	}
	after, err := os.ReadFile(filepath.Join(dir, "retained.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, evidence) {
		t.Fatalf("disabled bind changed sidecar: %q", after)
	}
}
