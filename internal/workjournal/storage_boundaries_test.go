package workjournal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestStorageRejectsExistingUnknownSchemaBeforeAppend(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	p := s.eventPath(1)
	prior := []byte(`{"schema":"future.work/v9","runtime_id":"r1","seq":1}` + "\n")
	if err := os.WriteFile(p, prior, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]Record{{Schema: Schema, Seq: 2}}, Checkpoint{Schema: Schema, LastSeq: 2}); err == nil {
		t.Fatal("accepted an existing segment with an unknown schema")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(prior) {
		t.Fatalf("unknown-schema segment was mutated: before=%q after=%q", prior, after)
	}
}

func TestStorageRetentionNeverPrunesOwnActiveSegment(t *testing.T) {
	d := t.TempDir()
	// Keep a foreign runtime sealed so retention may prune it, while r1 is
	// lexically earlier and remains the active writer's segment.
	foreign := work.WorkScope{SessionID: "s", RuntimeEpoch: "r2", ConversationEpoch: "c"}
	r := Record{Schema: Schema, SessionID: "s", RuntimeID: "r2", Seq: 1, At: time.Now().UTC(), Type: "foreign"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "events-r2-000001.jsonl"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cp, err := json.Marshal(Checkpoint{Schema: Schema, Authority: "diagnostic_only", Sealed: true, Snapshot: work.Snapshot{Scope: foreign}, LastSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "snapshot-r2.json"), append(cp, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	s := newStorage(d, testScope(), Options{SessionBytes: 1})
	if _, err := s.Write([]Record{{Schema: Schema, Seq: 1, Type: "active"}}, Checkpoint{Schema: Schema, LastSeq: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.eventPath(1)); err != nil {
		t.Fatalf("active segment was pruned: %v", err)
	}
}

func TestStorageRetentionGapsAreBounded(t *testing.T) {
	s := newStorage(t.TempDir(), testScope(), Options{})
	for i := 0; i < 300; i++ {
		s.addRetentionGap(Gap{RuntimeID: "runtime-" + string(rune('a'+i%26)) + string(rune(i/26)), From: uint64(i + 1), To: uint64(i + 1), Reason: "retention_prune"})
	}
	if got := len(s.health.Gaps); got > 128 {
		t.Fatalf("retention gaps grew without bound: %d", got)
	}
}

func TestStorageBatchSyncDoesNotRepeatPerRecord(t *testing.T) {
	s := newStorage(t.TempDir(), testScope(), Options{SegmentBytes: 1 << 20})
	syncs := 0
	s.io.sync = func(*os.File) error {
		syncs++
		return nil
	}
	records := make([]Record, 100)
	for i := range records {
		records[i] = Record{Schema: Schema, Seq: uint64(i + 1), Type: "batch"}
	}
	if _, err := s.Write(records, Checkpoint{Schema: Schema, LastSeq: 100}); err != nil {
		t.Fatal(err)
	}
	// One event segment, one checkpoint temp file, and the storage directory.
	if syncs != 3 {
		t.Fatalf("sync calls=%d, expected one per touched segment plus checkpoint and directory", syncs)
	}
}

func TestStorageShortWriteRollsBackActualBytes(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	originalWrite := s.io.write
	s.io.write = func(f *os.File, b []byte) (int, error) { return len(b) - 1, nil }
	if _, err := s.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1}); err == nil {
		t.Fatal("short write unexpectedly succeeded")
	}
	s.io.write = originalWrite
	if _, err := s.Write([]Record{{Schema: Schema, Seq: 2}}, Checkpoint{Schema: Schema, LastSeq: 2}); err != nil {
		t.Fatalf("valid append after rollback failed: %v", err)
	}
	rep, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Records) != 1 || rep.Records[0].Seq != 2 {
		t.Fatalf("partial short-write bytes survived: records=%+v notes=%v", rep.Records, rep.Notes)
	}
}
