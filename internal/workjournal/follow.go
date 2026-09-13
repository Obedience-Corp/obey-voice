package workjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FollowOptions struct{ JobID string }

type FollowNotice struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	RuntimeID string `json:"runtime_id,omitempty"`
	From      uint64 `json:"from,omitempty"`
	To        uint64 `json:"to,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type FollowEvent struct {
	Record *Record
	Notice *FollowNotice
}

// MatchJob inspects structured snapshot context rather than arbitrary payload
// substrings when selecting records for a job.
func MatchJob(r Record, id string) bool {
	if id == "" {
		return true
	}
	if r.JobID == id {
		return true
	}
	if r.Type != "work.snapshot" {
		return false
	}
	var body struct {
		Snapshot struct {
			Jobs []struct {
				Key struct {
					JobID string `json:"job_id"`
				} `json:"key"`
			} `json:"jobs"`
			Pending []struct {
				DeliveryKey struct {
					JobID string `json:"job_id"`
				} `json:"delivery_key"`
			} `json:"pending"`
			Communicated []struct {
				DeliveryKey struct {
					JobID string `json:"job_id"`
				} `json:"delivery_key"`
			} `json:"communicated"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(r.Data, &body) != nil {
		return false
	}
	for _, j := range body.Snapshot.Jobs {
		if j.Key.JobID == id {
			return true
		}
	}
	for _, v := range body.Snapshot.Pending {
		if v.DeliveryKey.JobID == id {
			return true
		}
	}
	for _, v := range body.Snapshot.Communicated {
		if v.DeliveryKey.JobID == id {
			return true
		}
	}
	return false
}

// MatchJobCorrelated also resolves delivery records, whose schema carries
// result IDs rather than a job ID. The correlation map is populated only from
// structured snapshot outcome fields and is scoped by runtime.
func MatchJobCorrelated(r Record, id string, results map[string]string) bool {
	if MatchJob(r, id) {
		return true
	}
	if r.Type != "work.delivery" {
		return false
	}
	var body struct {
		ResultIDs []string `json:"result_ids"`
	}
	if json.Unmarshal(r.Data, &body) != nil {
		return false
	}
	for _, result := range body.ResultIDs {
		if results[r.RuntimeID+"/"+r.ConversationEpoch+"/"+result] == id {
			return true
		}
	}
	return false
}

func learnJobResults(r Record, results map[string]string) {
	if r.Type != "work.snapshot" {
		return
	}
	var body struct {
		Snapshot struct {
			Jobs []struct {
				Key struct {
					JobID string `json:"job_id"`
				} `json:"key"`
				Outcome *struct {
					ResultID string `json:"result_id"`
					JobID    string `json:"job_id"`
				} `json:"outcome"`
			} `json:"jobs"`
			Pending []struct {
				Outcome struct {
					ResultID string `json:"result_id"`
					JobID    string `json:"job_id"`
				} `json:"outcome"`
			} `json:"pending"`
			Communicated []struct {
				Outcome struct {
					ResultID string `json:"result_id"`
					JobID    string `json:"job_id"`
				} `json:"outcome"`
			} `json:"communicated"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(r.Data, &body) != nil {
		return
	}
	for _, j := range body.Snapshot.Jobs {
		if j.Outcome != nil && j.Outcome.ResultID != "" {
			id := j.Outcome.JobID
			if id == "" {
				id = j.Key.JobID
			}
			results[r.RuntimeID+"/"+r.ConversationEpoch+"/"+j.Outcome.ResultID] = id
		}
	}
	for _, v := range body.Snapshot.Pending {
		if v.Outcome.ResultID != "" && v.Outcome.JobID != "" {
			results[r.RuntimeID+"/"+r.ConversationEpoch+"/"+v.Outcome.ResultID] = v.Outcome.JobID
		}
	}
	for _, v := range body.Snapshot.Communicated {
		if v.Outcome.ResultID != "" && v.Outcome.JobID != "" {
			results[r.RuntimeID+"/"+r.ConversationEpoch+"/"+v.Outcome.ResultID] = v.Outcome.JobID
		}
	}
}

func LearnJobResults(r Record, results map[string]string) { learnJobResults(r, results) }

func Follow(ctx context.Context, dir string, opts FollowOptions, emit func(FollowEvent) error) error {
	if ctx == nil {
		return errors.New("workjournal: nil follow context")
	}
	if emit == nil {
		return errors.New("workjournal: nil follow callback")
	}
	offsets := map[string]int64{}
	partial := map[string][]byte{}
	watermark := map[string]uint64{}
	results := map[string]string{}
	known := map[string]bool{}
	notified := map[string]bool{}
	checkpointMTime := map[string]time.Time{}
	retentionMTime := time.Time{}
	order := []string{}
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		if err := followOnce(ctx, dir, opts, emit, offsets, partial, watermark, results, known, notified, checkpointMTime, &retentionMTime, &order); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func followOnce(ctx context.Context, dir string, opts FollowOptions, emit func(FollowEvent) error, offsets map[string]int64, partial map[string][]byte, watermark map[string]uint64, results map[string]string, known, notified map[string]bool, checkpointMTime map[string]time.Time, retentionMTime *time.Time, order *[]string) error {
	if len(notified) > 4096 || len(checkpointMTime) > 4096 {
		clear(notified)
		clear(checkpointMTime)
		if err := emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", Reason: "notice_history_evicted"}}); err != nil {
			return err
		}
	}
	st, statErr := os.Lstat(dir)
	if statErr == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workjournal: journal directory is symlink")
	}
	if statErr != nil || !st.IsDir() {
		err := statErr
		if (errors.Is(err, os.ErrNotExist) || err == nil) && !notified["missing"] {
			notified["missing"] = true
			return emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", Reason: "history_unavailable"}})
		}
		if errors.Is(err, os.ErrNotExist) || err == nil {
			return nil
		}
		return err
	}
	checkpoints, _ := filepath.Glob(filepath.Join(dir, "snapshot-*.json"))
	for _, path := range checkpoints {
		st, e := os.Stat(path)
		if e != nil {
			continue
		}
		if old, ok := checkpointMTime[path]; ok && old.Equal(st.ModTime()) {
			continue
		}
		checkpointMTime[path] = st.ModTime()
		b, e := readRetentionFile(path, 1<<20)
		if e != nil {
			if !notified[path+"-read"] {
				notified[path+"-read"] = true
				if e = emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "snapshot-"), ".json"), Reason: "checkpoint_unreadable"}}); e != nil {
					return e
				}
			}
			continue
		}
		var cp Checkpoint
		if json.Unmarshal(b, &cp) != nil {
			if !notified[path+"-malformed"] {
				notified[path+"-malformed"] = true
				if e = emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "snapshot-"), ".json"), Reason: "checkpoint_malformed"}}); e != nil {
					return e
				}
			}
			continue
		}
		if cp.Schema != Schema {
			return fmt.Errorf("%w in %s", ErrUnsupportedSchema, filepath.Base(path))
		}
		if runtime, ok := snapshotRuntime(path); !ok || cp.Snapshot.Scope.RuntimeEpoch != runtime {
			return fmt.Errorf("checkpoint runtime does not match %s", filepath.Base(path))
		}
		seed := Record{Schema: Schema, RuntimeID: cp.Snapshot.Scope.RuntimeEpoch, ConversationEpoch: cp.Snapshot.Scope.ConversationEpoch, Type: "work.snapshot", Data: mustJSON(map[string]any{"snapshot": cp.Snapshot})}
		learnJobResults(seed, results)
		if err := trimJobResults(results, emit); err != nil {
			return err
		}
		if cp.Sealed && !notified[path] {
			notified[path] = true
			if e = emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.retired", RuntimeID: cp.Snapshot.Scope.RuntimeEpoch, Reason: "runtime_sealed"}}); e != nil {
				return e
			}
		}
		for i, g := range cp.Health.Gaps {
			key := fmt.Sprintf("%s-gap-%d-%d-%d-%s", path, i, g.From, g.To, g.Reason)
			if notified[key] {
				continue
			}
			notified[key] = true
			if e := emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: g.RuntimeID, From: g.From, To: g.To, Reason: g.Reason}}); e != nil {
				return e
			}
		}
		if cp.Health.LastError != "" && !notified[path+"-error"] {
			notified[path+"-error"] = true
			if e := emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: cp.Snapshot.Scope.RuntimeEpoch, Reason: "storage_error"}}); e != nil {
				return e
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	present := map[string]bool{}
	for _, path := range files {
		present[path] = true
		if err := followFile(ctx, path, opts, emit, offsets, partial, watermark, results, order); err != nil {
			return err
		}
		known[path] = true
	}
	for path := range known {
		if present[path] {
			continue
		}
		runtime := runtimeFromPath(path)
		if err := emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: runtime, From: watermark[runtime] + 1, Reason: "segment_pruned"}}); err != nil {
			return err
		}
		delete(known, path)
		delete(offsets, path)
		delete(partial, path)
	}
	if st, err := os.Stat(filepath.Join(dir, "retention.json")); err == nil && !st.ModTime().Equal(*retentionMTime) {
		*retentionMTime = st.ModTime()
		if err := emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", Reason: "retention_prune"}}); err != nil {
			return err
		}
	}

	return nil
}

func followFile(ctx context.Context, path string, opts FollowOptions, emit func(FollowEvent) error, offsets map[string]int64, partial map[string][]byte, watermark map[string]uint64, results map[string]string, order *[]string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workjournal: invalid event segment")
	}
	start := offsets[path]
	if start > st.Size() {
		start = 0
		offsets[path] = 0
		partial[path] = nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		return err
	}
	data := append(append([]byte(nil), partial[path]...), b...)
	var tail []byte
	for i, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if line[len(line)-1] != '\n' {
			tail = append([]byte(nil), line...)
			break
		}
		var r Record
		if err = json.Unmarshal(bytes.TrimSpace(line), &r); err != nil {
			return fmt.Errorf("%s line %d: malformed record: %w", filepath.Base(path), i+1, err)
		}
		if r.Schema != Schema {
			return fmt.Errorf("%w %q in %s", ErrUnsupportedSchema, r.Schema, filepath.Base(path))
		}
		if runtime, ok := segmentRuntime(path); !ok || r.RuntimeID != runtime || r.Seq == 0 {
			return fmt.Errorf("%s line %d: missing runtime/sequence", filepath.Base(path), i+1)
		}
		if r.Seq <= watermark[r.RuntimeID] {
			continue
		}
		learnJobResults(r, results)
		if err := trimJobResults(results, emit); err != nil {
			return err
		}
		if watermark[r.RuntimeID]+1 < r.Seq {
			if err = emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: r.RuntimeID, From: watermark[r.RuntimeID] + 1, To: r.Seq - 1, Reason: "sequence_gap"}}); err != nil {
				return err
			}
		}
		watermark[r.RuntimeID] = r.Seq
		found := false
		for _, id := range *order {
			if id == r.RuntimeID {
				found = true
				break
			}
		}
		if !found {
			*order = append(*order, r.RuntimeID)
		}
		if len(*order) > 256 {
			old := (*order)[0]
			*order = (*order)[1:]
			delete(watermark, old)
			if err = emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", RuntimeID: old, Reason: "watermark_evicted"}}); err != nil {
				return err
			}
		}
		if opts.JobID != "" && !MatchJobCorrelated(r, opts.JobID, results) {
			continue
		}
		if err = emit(FollowEvent{Record: &r}); err != nil {
			return err
		}
	}
	if len(tail) > 1<<20 {
		return errors.New("workjournal: unterminated record exceeds follow limit")
	}
	partial[path] = tail
	offsets[path] = start + int64(len(b))
	return ctx.Err()
}

func runtimeFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	base = strings.TrimPrefix(base, "events-")
	if i := strings.LastIndexByte(base, '-'); i >= 0 {
		return base[:i]
	}
	return base
}
func FollowJSON(ev FollowEvent) ([]byte, error) {
	if ev.Notice != nil {
		return json.Marshal(ev.Notice)
	}
	if ev.Record == nil {
		return nil, errors.New("workjournal: empty follow event")
	}
	return json.Marshal(ev.Record)
}

func trimJobResults(results map[string]string, emit func(FollowEvent) error) error {
	if len(results) <= 4096 {
		return nil
	}
	keys := make([]string, 0, len(results))
	for key := range results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys[:len(keys)-4096] {
		delete(results, key)
	}
	return emit(FollowEvent{Notice: &FollowNotice{Schema: Schema, Type: "journal.gap", Reason: "job_correlation_evicted"}})
}
