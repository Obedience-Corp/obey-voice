package workjournal

import (
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func testScope() work.WorkScope {
	return work.WorkScope{SessionID: "s", RuntimeEpoch: "r1", ConversationEpoch: "c"}
}
func TestStorageWriteReadAndDetail(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{SegmentBytes: 1 << 20})
	r := Record{Schema: Schema, SessionID: "s", RuntimeID: "r1", Seq: 1, At: time.Now(), Type: "job.started"}
	cp := Checkpoint{Schema: Schema, Authority: "diagnostic_only", LastSeq: 1}
	if _, err := s.Write([]Record{r}, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Detail("x", "secret removed", 20); err != nil {
		t.Fatal(err)
	}
	got, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || len(got.Checkpoints) != 1 || !got.PersistedOnly {
		t.Fatalf("report=%+v", got)
	}
	b, err := ReadDetail(d, "x")
	if err != nil || string(b) != "secret removed" {
		t.Fatalf("detail %q %v", b, err)
	}
	if _, err := ReadDetail(d, "../x"); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestStorageRepairsIncompleteTail(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	p := s.eventPath(1)
	if err := os.WriteFile(p, []byte(`{"schema":"obey-voice.work/v1","runtime_id":"r1","seq":1}`+"\n"+`{"schema":"obey-voice.work/v1"`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]Record{{Schema: Schema, SessionID: "s", RuntimeID: "r1", Seq: 2}}, Checkpoint{Schema: Schema, LastSeq: 2}); err != nil {
		t.Fatal(err)
	}
	rep, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Records) != 2 {
		t.Fatalf("records=%d notes=%v", len(rep.Records), rep.Notes)
	}
}
func TestReaderConflictingDuplicate(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "events-r1-000001.jsonl")
	a := `{"schema":"obey-voice.work/v1","runtime_id":"r1","seq":1,"type":"a"}` + "\n"
	b := `{"schema":"obey-voice.work/v1","runtime_id":"r1","seq":1,"type":"b"}` + "\n"
	if err := os.WriteFile(p, []byte(a+b), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Incomplete || len(rep.Records) != 1 {
		t.Fatalf("report=%+v", rep)
	}
}

func TestStorageSyncFailureDoesNotAdvanceWatermark(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	s.io.sync = func(*os.File) error { return os.ErrPermission }
	h, err := s.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1})
	if err == nil || h.PersistedSeq != 0 {
		t.Fatalf("health=%+v err=%v", h, err)
	}
	if _, e := os.Stat(filepath.Join(d, "snapshot-r1.json")); !os.IsNotExist(e) {
		t.Fatal("checkpoint advanced")
	}
}
func TestStorageShortWrite(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	s.io.write = func(f *os.File, b []byte) (int, error) { return len(b) - 1, nil }
	h, err := s.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1})
	if err == nil || h.PersistedSeq != 0 {
		t.Fatalf("health=%+v err=%v", h, err)
	}
}
func TestStorageRenameFailurePreservesCheckpoint(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{})
	if _, e := s.Write(nil, Checkpoint{Schema: Schema, LastSeq: 1}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(filepath.Join(d, "snapshot-r1.json"))
	s.io.rename = func(_, _ string) error { return os.ErrPermission }
	if _, e := s.Write(nil, Checkpoint{Schema: Schema, LastSeq: 2}); e == nil {
		t.Fatal("rename unexpectedly passed")
	}
	after, _ := os.ReadFile(filepath.Join(d, "snapshot-r1.json"))
	if string(before) != string(after) {
		t.Fatal("checkpoint replaced on rename failure")
	}
}
func TestStorageRetentionAcrossRuntimes(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{SegmentBytes: 1, SessionBytes: 2})
	_, _ = s.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1, Sealed: true})
	other := newStorage(d, work.WorkScope{SessionID: "s", RuntimeEpoch: "r2", ConversationEpoch: "c"}, Options{SegmentBytes: 1, SessionBytes: 2})
	_, _ = other.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1})
	files, _ := filepath.Glob(filepath.Join(d, "events-*.jsonl"))
	if len(files) > 2 {
		t.Fatalf("retention left %d files", len(files))
	}
	if len(other.health.Gaps) == 0 || other.health.Gaps[0].RuntimeID == "" || other.health.Gaps[0].From == 0 || other.health.Gaps[0].To < other.health.Gaps[0].From {
		t.Fatalf("retention gap=%+v", other.health.Gaps)
	}
}

func TestStorageCrashBoundariesSubprocess(t *testing.T) {
	if os.Getenv("WORKJOURNAL_CRASH_FIXTURE") != "" {
		runCrashFixture(t)
		return
	}
	for _, mode := range []string{"append", "rename"} {
		d := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=TestStorageCrashBoundariesSubprocess", "-test.count=1")
		cmd.Env = append(os.Environ(), "WORKJOURNAL_CRASH_FIXTURE="+mode, "WORKJOURNAL_CRASH_DIR="+d)
		if err := cmd.Run(); err == nil {
			t.Fatalf("fixture %s did not exit", mode)
		}
		rep, err := Read(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Checkpoints) != 1 || rep.Checkpoints[0].LastSeq != 1 {
			t.Fatalf("mode %s checkpoints=%+v", mode, rep.Checkpoints)
		}
		if mode == "append" && !rep.Incomplete {
			t.Fatalf("append crash not reported incomplete: %+v", rep)
		}
	}
}
func runCrashFixture(t *testing.T) {
	d := os.Getenv("WORKJOURNAL_CRASH_DIR")
	s := newStorage(d, testScope(), Options{})
	if _, e := s.Write([]Record{{Schema: Schema, Seq: 1}}, Checkpoint{Schema: Schema, LastSeq: 1, At: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	switch os.Getenv("WORKJOURNAL_CRASH_FIXTURE") {
	case "append":
		p := s.eventPath(1)
		f, e := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = f.Write([]byte(`{"schema":"obey-voice.work/v1","runtime_id":"r1","seq":2`))
		_ = f.Close()
		os.Exit(91)
	case "rename":
		tmp := filepath.Join(d, ".snapshot-r1.tmp")
		if e := os.WriteFile(tmp, []byte(`{"schema":"obey-voice.work/v1","last_seq":99}`), 0600); e != nil {
			t.Fatal(e)
		}
		os.Exit(92)
	}
}

func TestStorageRecordDetailRoundTripAndLimit(t *testing.T) {
	d := t.TempDir()
	s := newStorage(d, testScope(), Options{DetailBytes: 1024})
	r := Record{Schema: Schema, SessionID: "s", RuntimeID: "r1", Seq: 7, CallID: "call", Data: json.RawMessage(`{"tool":"x"}`), DetailPayload: &DetailPayload{Text: "redacted tool output", OriginalBytes: 19}}
	if _, e := s.Write([]Record{r}, Checkpoint{Schema: Schema, LastSeq: 7}); e != nil {
		t.Fatal(e)
	}
	rep, e := Read(d)
	if e != nil || len(rep.Records) != 1 {
		t.Fatal(rep, e)
	}
	var obj map[string]json.RawMessage
	if e = json.Unmarshal(rep.Records[0].Data, &obj); e != nil {
		t.Fatal(e)
	}
	var meta Detail
	if e = json.Unmarshal(obj["details"], &meta); e != nil || meta.Ref == "" {
		t.Fatalf("meta=%+v err=%v", meta, e)
	}
	b, e := ReadDetail(d, meta.Ref)
	if e != nil || string(b) != "redacted tool output" {
		t.Fatalf("detail=%q err=%v", b, e)
	}
	d2 := t.TempDir()
	s2 := newStorage(d2, testScope(), Options{DetailBytes: 1024, SessionDetailBytes: 2})
	r.Seq = 8
	if _, e = s2.Write([]Record{r}, Checkpoint{Schema: Schema, LastSeq: 8}); e != nil {
		t.Fatal(e)
	}
	rep, e = Read(d2)
	if e != nil {
		t.Fatal(e)
	}
	obj = nil
	_ = json.Unmarshal(rep.Records[0].Data, &obj)
	meta = Detail{}
	_ = json.Unmarshal(obj["details"], &meta)
	if meta.Unavailable != "retention_limit" {
		t.Fatalf("expected retention_limit: %+v", meta)
	}
}
