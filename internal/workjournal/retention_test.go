package workjournal

import (
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeSealed(t *testing.T, root, name string, at time.Time, size int) {
	t.Helper()
	d := filepath.Join(root, name+".work")
	scope := work.WorkScope{SessionID: name, RuntimeEpoch: "r", ConversationEpoch: "c"}
	s := newStorage(d, scope, Options{SegmentBytes: 1})
	if _, e := s.Write([]Record{{Schema: Schema, SessionID: name, RuntimeID: "r", Seq: 1, Data: json.RawMessage(`{"x":"` + strings.Repeat("x", size) + `"}`)}}, Checkpoint{Schema: Schema, Authority: "diagnostic_only", LastSeq: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Detail("ref", "detail", 6); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "snapshot-r.json")
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var cp Checkpoint
	if e = json.Unmarshal(b, &cp); e != nil {
		t.Fatal(e)
	}
	cp.At = at
	cp.Sealed = true
	b, _ = json.Marshal(cp)
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func sidecarSize(t *testing.T, d string) int64 {
	var n int64
	if e := filepath.Walk(d, func(p string, i os.FileInfo, e error) error {
		if e == nil && i != nil {
			if i.Mode().IsRegular() {
				n += i.Size()
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestPruneSealedExpiryAndPreservation(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	makeSealed(t, root, "old", old, 20)

	active := filepath.Join(root, "active.work")
	makeSealed(t, root, "active", old, 20)
	cp := filepath.Join(active, "snapshot-r.json")
	b, _ := os.ReadFile(cp)
	var c Checkpoint
	_ = json.Unmarshal(b, &c)
	c.Sealed = false
	b, _ = json.Marshal(c)
	_ = os.WriteFile(cp, b, 0600)
	_ = os.WriteFile(filepath.Join(root, "active.json"), []byte("canonical"), 0600)
	if e := PruneSealed(root, active, Options{ExpireAfter: time.Hour}, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "old.work", "events-r-000001.jsonl")); !os.IsNotExist(e) {
		t.Fatal("old events retained")
	}
	if _, e := os.Stat(filepath.Join(root, "old.work", "snapshot-r.json")); e != nil {
		t.Fatal("checkpoint removed")
	}
	if _, e := ReadDetail(filepath.Join(root, "old.work"), "ref"); !os.IsNotExist(e) {
		t.Fatalf("pruned detail still readable: %v", e)
	}
	if _, e := os.Stat(filepath.Join(active, "events-r-000001.jsonl")); e != nil {
		t.Fatal("active removed")
	}
}
func TestPruneSealedGlobalBudgetOldestFirst(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	makeSealed(t, root, "old", now.Add(-3*time.Hour), 20)
	makeSealed(t, root, "new", now, 20)
	budget := sidecarSize(t, filepath.Join(root, "new.work"))
	if e := PruneSealed(root, "", Options{ExpireAfter: time.Hour, GlobalSealedBytes: budget}, now); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(root, "old.work", "events-r-000001.jsonl")); !os.IsNotExist(e) {
		t.Fatal("oldest was not pruned")
	}
	if _, e := os.Stat(filepath.Join(root, "new.work", "events-r-000001.jsonl")); e != nil {
		t.Fatal("newest sidecar was pruned")
	}
}
func TestPruneSealedUnknownPreserved(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "unknown.work")
	_ = os.MkdirAll(d, 0700)
	_ = os.WriteFile(filepath.Join(d, "snapshot-r.json"), []byte(`{"schema":"future","sealed":true}`), 0600)
	if e := PruneSealed(root, "", Options{ExpireAfter: time.Hour}, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(d, "snapshot-r.json")); e != nil {
		t.Fatal("unknown schema removed")
	}
}
