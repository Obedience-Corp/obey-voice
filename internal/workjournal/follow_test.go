package workjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func followRecord(seq uint64) []byte {
	b, _ := json.Marshal(Record{Schema: Schema, SessionID: "s", RuntimeID: "r1", Seq: seq, At: time.Now().UTC(), Type: "job.started", JobID: "job-1"})
	return append(b, '\n')
}

func waitFollow(t *testing.T, ch <-chan FollowEvent) FollowEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not emit event")
		return FollowEvent{}
	}
}

func TestFollowReadsRotatedSegmentsAndDeduplicates(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "events-r1-000001.jsonl"), followRecord(1), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	if got := waitFollow(t, events).Record.Seq; got != 1 {
		t.Fatalf("first seq=%d", got)
	}
	if err := os.WriteFile(filepath.Join(d, "events-r1-000002.jsonl"), followRecord(2), 0600); err != nil {
		t.Fatal(err)
	}
	if got := waitFollow(t, events).Record.Seq; got != 2 {
		t.Fatalf("rotated seq=%d", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v, want cancellation", err)
	}
}

func TestFollowWaitsForUnterminatedLineCompletion(t *testing.T) {
	d := t.TempDir()
	line := followRecord(1)
	path := filepath.Join(d, "events-r1-000001.jsonl")
	if err := os.WriteFile(path, line[:len(line)-1], 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent, 2)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	select {
	case ev := <-events:
		t.Fatalf("emitted torn event: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{'\n'}); err == nil {
		err = f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := waitFollow(t, events).Record.Seq; got != 1 {
		t.Fatalf("completed seq=%d", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v, want cancellation", err)
	}
}

func TestFollowPreservesPartialAfterCommittedLineInSameRead(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "events-r1-000001.jsonl")
	first, second := followRecord(1), followRecord(2)
	if err := os.WriteFile(path, append(first, second[:len(second)-1]...), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	if got := waitFollow(t, events).Record.Seq; got != 1 {
		t.Fatalf("committed seq=%d", got)
	}
	select {
	case ev := <-events:
		t.Fatalf("emitted incomplete seq=%v", ev.Record)
	case <-time.After(150 * time.Millisecond):
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{'\n'}); err == nil {
		err = f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := waitFollow(t, events).Record.Seq; got != 2 {
		t.Fatalf("completed seq=%d", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v", err)
	}
}

func TestFollowReportsMissingSequenceRange(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "events-r1-000001.jsonl"), append(followRecord(1), followRecord(3)...), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	if got := waitFollow(t, events).Record.Seq; got != 1 {
		t.Fatalf("first seq=%d", got)
	}
	n := waitFollow(t, events).Notice
	if n == nil || n.Type != "journal.gap" || n.From != 2 || n.To != 2 || n.Reason != "sequence_gap" {
		t.Fatalf("gap notice=%+v", n)
	}
	if got := waitFollow(t, events).Record.Seq; got != 3 {
		t.Fatalf("last seq=%d", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v", err)
	}
}

func TestMatchJobUsesSnapshotContext(t *testing.T) {
	r := Record{Schema: Schema, Type: "work.snapshot", Data: json.RawMessage(`{"snapshot":{"jobs":[{"key":{"job_id":"target"}}]}}`)}
	if !MatchJob(r, "target") {
		t.Fatal("snapshot containing job was filtered")
	}
	if MatchJob(r, "other") {
		t.Fatal("unrelated snapshot matched")
	}
	// A payload mentioning an ID in an unrecognized field is not job context.
	r.Data = json.RawMessage(`{"snapshot":{"jobs":[]},"message":"target"}`)
	if MatchJob(r, "target") {
		t.Fatal("raw substring was treated as job context")
	}
}

func TestMatchJobCorrelatesDeliveryResultToSnapshotJob(t *testing.T) {
	results := map[string]string{}
	snapshot := Record{Schema: Schema, RuntimeID: "r1", Type: "work.snapshot", Data: json.RawMessage(`{"snapshot":{"jobs":[{"key":{"job_id":"target"},"outcome":{"result_id":"result-1","job_id":"target"}}]}}`)}
	LearnJobResults(snapshot, results)
	delivery := Record{Schema: Schema, RuntimeID: "r1", Type: "work.delivery", Data: json.RawMessage(`{"result_ids":["result-1"]}`)}
	if !MatchJobCorrelated(delivery, "target", results) {
		t.Fatal("delivery result was not correlated to its job")
	}
	if MatchJobCorrelated(delivery, "other", results) {
		t.Fatal("delivery matched unrelated job")
	}
}

func TestFollowReportsPrunedSegmentAndCancellationIsBounded(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "events-r1-000001.jsonl")
	if err := os.WriteFile(path, followRecord(1), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan FollowEvent, 4)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	if got := waitFollow(t, events).Record.Seq; got != 1 {
		t.Fatalf("initial seq=%d", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	notice := waitFollow(t, events).Notice
	if notice == nil || notice.Type != "journal.gap" || notice.Reason != "segment_pruned" || notice.From != 1 || notice.To != 1 {
		t.Fatalf("prune notice=%+v", notice)
	}
	started := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancellation was not bounded")
	}
}

func TestFollowReportsConflictingDuplicate(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "events-r1-000001.jsonl")
	if err := os.WriteFile(path, followRecord(1), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, d, FollowOptions{}, func(ev FollowEvent) error { events <- ev; return nil })
	}()
	if ev := waitFollow(t, events); ev.Record == nil || ev.Record.Seq != 1 {
		t.Fatalf("initial event=%+v", ev)
	}
	// A rotated segment repeats the identity with a different payload.
	changed := followRecord(1)
	changed = bytes.Replace(changed, []byte("job.started"), []byte("job.finished"), 1)
	if err := os.WriteFile(filepath.Join(d, "events-r1-000002.jsonl"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	n := waitFollow(t, events).Notice
	if n == nil || n.Reason != "conflicting_duplicate" || n.From != 1 || n.To != 1 {
		t.Fatalf("duplicate notice=%+v", n)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error=%v", err)
	}
}
