package workjournal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestReadRejectsRecordFromDifferentSegmentRuntime(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "events-runtime-a-000001.jsonl")
	good := Record{Schema: Schema, RuntimeID: "runtime-a", Seq: 1, At: time.Now().UTC()}
	foreign := Record{Schema: Schema, RuntimeID: "runtime-b", Seq: 2, At: time.Now().UTC()}
	b1, _ := json.Marshal(good)
	b2, _ := json.Marshal(foreign)
	if err := os.WriteFile(path, append(append(b1, '\n'), append(b2, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := Read(d)
	if err == nil || len(rep.Records) != 1 || rep.Records[0].RuntimeID != "runtime-a" || !rep.Incomplete {
		t.Fatalf("report=%+v err=%v; wanted valid prefix and identity failure", rep, err)
	}
}

func TestStorageRejectsRecordFromDifferentScopeBeforeAppend(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "events-r1-000001.jsonl")
	r := Record{Schema: Schema, RuntimeID: "r2", Seq: 1, At: time.Now().UTC()}
	b, _ := json.Marshal(r)
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	s := newStorage(d, testScope(), Options{})
	if _, err := s.Write(nil, Checkpoint{Schema: Schema, LastSeq: 1}); err == nil {
		t.Fatal("accepted segment containing a different runtime")
	}
	if s.initialized {
		t.Fatal("storage initialized after identity mismatch")
	}
	valid := newStorage(t.TempDir(), testScope(), Options{})
	if _, err := valid.Write(nil, Checkpoint{Schema: Schema, Snapshot: work.Snapshot{Scope: work.WorkScope{RuntimeEpoch: "r2"}}}); err == nil {
		t.Fatal("accepted checkpoint from a different runtime")
	}
}

func TestReadRejectsMismatchedCheckpointRuntime(t *testing.T) {
	d := t.TempDir()
	cp := Checkpoint{Schema: Schema, Snapshot: work.Snapshot{Scope: work.WorkScope{RuntimeEpoch: "runtime-b"}}, LastSeq: 1}
	b, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(d, "snapshot-runtime-a.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := Read(d)
	if err == nil || len(rep.Checkpoints) != 0 || !rep.Incomplete {
		t.Fatalf("report=%+v err=%v; wanted checkpoint identity failure", rep, err)
	}
}
