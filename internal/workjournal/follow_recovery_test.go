package workjournal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestFollowSeedsRetainedDeliveryFromCheckpoint(t *testing.T) {
	dir := t.TempDir()
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r1", ConversationEpoch: "c"}
	cp := Checkpoint{Schema: Schema, Snapshot: work.Snapshot{Scope: scope, Pending: []work.ResultView{{Outcome: work.Outcome{JobID: "target", ResultID: "result"}}}}}
	b, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(dir, "snapshot-r1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	r := Record{Schema: Schema, RuntimeID: "r1", ConversationEpoch: "c", Seq: 9, Type: "work.delivery", Data: json.RawMessage(`{"result_ids":["result"],"stage":"held"}`)}
	b, _ = json.Marshal(r)
	if err := os.WriteFile(filepath.Join(dir, "events-r1-000002.jsonl"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := false
	err := Follow(ctx, dir, FollowOptions{JobID: "target"}, func(ev FollowEvent) error {
		if ev.Record != nil {
			got = true
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || !got {
		t.Fatalf("retained delivery missing: got=%v err=%v", got, err)
	}
}

func TestFollowMissingDirectoryCanAppearLater(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := false
	err := Follow(ctx, dir, FollowOptions{}, func(ev FollowEvent) error {
		if ev.Notice != nil && ev.Notice.Reason == "history_unavailable" {
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "events-r1-000001.jsonl"), followRecord(1), 0600)
		}
		if ev.Record != nil {
			got = true
			cancel()
		}
		return nil
	})
	if !got || !errors.Is(err, context.Canceled) {
		t.Fatalf("follow did not recover: got=%v err=%v", got, err)
	}
}

func TestFollowRejectsUnknownCheckpointAndOversizedTail(t *testing.T) {
	for _, kind := range []string{"schema", "tail", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			switch kind {
			case "schema":
				_ = os.WriteFile(filepath.Join(dir, "snapshot-r1.json"), []byte(`{"schema":"future"}`), 0600)
			case "tail":
				_ = os.WriteFile(filepath.Join(dir, "events-r1-000001.jsonl"), []byte(strings.Repeat("x", (1<<20)+1)), 0600)
			case "runtime":
				_ = os.WriteFile(filepath.Join(dir, "events-other-000001.jsonl"), followRecord(1), 0600)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Follow(ctx, dir, FollowOptions{}, func(FollowEvent) error { return nil })
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("invalid history accepted: %v", err)
			}
			if kind == "schema" && !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("schema error=%v", err)
			}
		})
	}
}

func TestJobCorrelationFencesConversation(t *testing.T) {
	results := map[string]string{}
	r := Record{RuntimeID: "same-runtime", ConversationEpoch: "old", Type: "work.snapshot", Data: json.RawMessage(`{"snapshot":{"pending":[{"outcome":{"job_id":"target","result_id":"reused"}}]}}`)}
	LearnJobResults(r, results)
	delivery := Record{RuntimeID: r.RuntimeID, ConversationEpoch: "new", Type: "work.delivery", Data: json.RawMessage(`{"result_ids":["reused"]}`)}
	if MatchJobCorrelated(delivery, "target", results) {
		t.Fatal("old conversation result matched new delivery")
	}
}
