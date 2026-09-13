package workjournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeReaderFile(t *testing.T, d string, lines ...string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(d, "events-r-000001.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
}
func validLine(seq uint64) string {
	return `{"schema":"obey-voice.work/v1","runtime_id":"r","seq":` + fmt.Sprint(seq) + `}`
}
func TestReaderTornFinalAndMalformedMiddle(t *testing.T) {
	d := t.TempDir()
	writeReaderFile(t, d, validLine(1), `{"schema":"obey-voice.work/v1","runtime_id":"r","seq":2`, validLine(3))
	rep, e := Read(d)
	if e == nil || len(rep.Records) != 1 || !rep.Incomplete {
		t.Fatalf("rep=%+v err=%v", rep, e)
	}
	d2 := t.TempDir()
	if e := os.WriteFile(filepath.Join(d2, "events-r-000001.jsonl"), []byte(validLine(1)+"\n"+validLine(2)+" partial"), 0600); e != nil {
		t.Fatal(e)
	}
	rep, e = Read(d2)
	if e != nil || len(rep.Records) != 1 || !rep.Incomplete {
		t.Fatalf("torn rep=%+v err=%v", rep, e)
	}
}
func TestReaderSchemaAndFirstGap(t *testing.T) {
	d := t.TempDir()
	writeReaderFile(t, d, `{"schema":"future","runtime_id":"r","seq":1}`)
	_, e := Read(d)
	if !errors.Is(e, ErrUnsupportedSchema) {
		t.Fatalf("err=%v", e)
	}
	d2 := t.TempDir()
	writeReaderFile(t, d2, validLine(2))
	rep, e := Read(d2)
	if e != nil || !rep.Incomplete {
		t.Fatalf("rep=%+v err=%v", rep, e)
	}
}
func TestReaderCheckpointHealthAndUnsealed(t *testing.T) {
	d := t.TempDir()
	cp := Checkpoint{Schema: Schema, Snapshot: work.Snapshot{Scope: work.WorkScope{RuntimeEpoch: "r"}}, LastSeq: 1, At: time.Now(), Health: Health{Gaps: []Gap{{RuntimeID: "r", From: 1, To: 1, Reason: "drop"}}}}
	b, _ := json.Marshal(cp)
	if e := os.WriteFile(filepath.Join(d, "snapshot-r.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	rep, e := Read(d)
	if e != nil || !rep.Incomplete || len(rep.Checkpoints) != 1 {
		t.Fatalf("rep=%+v err=%v", rep, e)
	}
}
func TestReaderMissingAndDetailSafety(t *testing.T) {
	rep, e := Read(filepath.Join(t.TempDir(), "missing"))
	if e != nil || !rep.Incomplete {
		t.Fatal(rep, e)
	}
	d := t.TempDir()
	if e := os.Mkdir(filepath.Join(d, "details"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(d, "details", "x.txt"), []byte(strings.Repeat("x", 64<<10+1)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadDetail(d, "x"); e == nil {
		t.Fatal("overlimit accepted")
	}
	os.RemoveAll(filepath.Join(d, "details"))
	if e := os.Symlink(t.TempDir(), filepath.Join(d, "details")); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadDetail(d, "x"); e == nil {
		t.Fatal("symlink accepted")
	}
}
