package workjournal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// readJSONL never treats an unterminated final line as committed history.
// Malformed middle records preserve the useful prefix and report the boundary.
func readJSONL(path string) ([]Record, bool, error) {
	b, err := readRetentionFile(path, 32<<20)
	if err != nil {
		return nil, false, err
	}
	var out []Record
	lines := bytes.SplitAfter(b, []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		if line[len(line)-1] != '\n' {
			return out, true, nil
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return out, false, fmt.Errorf("%s line %d: malformed record: %w", filepath.Base(path), i+1, err)
		}
		if r.Schema != Schema {
			return out, false, fmt.Errorf("%w %q in %s", ErrUnsupportedSchema, r.Schema, filepath.Base(path))
		}
		if r.RuntimeID == "" || r.Seq == 0 {
			return out, false, fmt.Errorf("%s line %d: missing runtime/sequence", filepath.Base(path), i+1)
		}
		out = append(out, r)
	}
	return out, false, nil
}

// segmentRuntime extracts the runtime identity from an on-disk segment name.
// Runtime IDs may contain hyphens, so the final hyphen separates the numeric
// segment index.
func segmentRuntime(path string) (string, bool) {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "events-") || !strings.HasSuffix(base, ".jsonl") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(base, "events-"), ".jsonl")
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return "", false
	}
	if _, err := strconv.ParseUint(name[i+1:], 10, 32); err != nil {
		return "", false
	}
	return name[:i], true
}

func readJSONLForRuntime(path, expected string) ([]Record, bool, error) {
	records, torn, err := readJSONL(path)
	if expected == "" {
		return records, torn, err
	}
	for i, r := range records {
		if r.RuntimeID != expected {
			return records[:i], torn, fmt.Errorf("%s: runtime %q does not match segment runtime %q", filepath.Base(path), r.RuntimeID, expected)
		}
	}
	return records, torn, err
}

func snapshotRuntime(path string) (string, bool) {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "snapshot-") || !strings.HasSuffix(base, ".json") {
		return "", false
	}
	runtime := strings.TrimSuffix(strings.TrimPrefix(base, "snapshot-"), ".json")
	if runtime == "" {
		return "", false
	}
	return runtime, true
}

func Read(dir string) (Report, error) {
	rep := Report{Schema: Schema, Source: dir, PersistedOnly: true}
	st, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		rep.Incomplete = true
		rep.Notes = []string{"Work history unavailable for this session"}
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return rep, errors.New("invalid work diagnostic directory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil {
		return rep, err
	}
	sort.Strings(files)
	seen := map[string]map[uint64]Record{}
	var failures []error
	var total int64
	for _, p := range files {
		st, err := os.Lstat(p)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		total += st.Size()
		if total > 64<<20 {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Diagnostic read limit reached; remaining segments omitted")
			break
		}
		expected, valid := segmentRuntime(p)
		if !valid {
			failures = append(failures, fmt.Errorf("invalid segment filename %s", filepath.Base(p)))
			continue
		}
		records, torn, err := readJSONLForRuntime(p, expected)
		if err != nil {
			failures = append(failures, err)
		}
		if torn {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Torn final line ignored in "+filepath.Base(p))
		}
		for _, r := range records {
			if seen[r.RuntimeID] == nil {
				seen[r.RuntimeID] = map[uint64]Record{}
			}
			if old, ok := seen[r.RuntimeID][r.Seq]; ok {
				a, _ := json.Marshal(old)
				b, _ := json.Marshal(r)
				if !bytes.Equal(a, b) {
					rep.Incomplete = true
					rep.Notes = append(rep.Notes, fmt.Sprintf("Conflicting duplicate %s/%d", r.RuntimeID, r.Seq))
				}
				continue
			}
			seen[r.RuntimeID][r.Seq] = r
			rep.Records = append(rep.Records, r)
		}
	}
	sort.Slice(rep.Records, func(i, j int) bool {
		a, b := rep.Records[i], rep.Records[j]
		if a.RuntimeID == b.RuntimeID {
			return a.Seq < b.Seq
		}
		return a.RuntimeID < b.RuntimeID
	})
	previous := map[string]uint64{}
	for _, r := range rep.Records {
		if r.Seq > previous[r.RuntimeID]+1 {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, fmt.Sprintf("Missing sequence range %s/%d-%d", r.RuntimeID, previous[r.RuntimeID]+1, r.Seq-1))
		}
		previous[r.RuntimeID] = r.Seq
	}
	checkpoints, err := filepath.Glob(filepath.Join(dir, "snapshot-*.json"))
	if err != nil {
		return rep, err
	}
	for _, p := range checkpoints {
		st, statErr := os.Lstat(p)
		if statErr != nil {
			failures = append(failures, statErr)
			continue
		}
		total += st.Size()
		if total > 64<<20 {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Diagnostic read limit reached; remaining checkpoints omitted")
			break
		}
		b, err := readRetentionFile(p, 32<<20)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var cp Checkpoint
		if err = json.Unmarshal(b, &cp); err != nil {
			failures = append(failures, err)
			continue
		}
		if cp.Schema != Schema {
			failures = append(failures, fmt.Errorf("%w %q", ErrUnsupportedSchema, cp.Schema))
			continue
		}
		if expected, ok := snapshotRuntime(p); ok && cp.Snapshot.Scope.RuntimeEpoch != "" && cp.Snapshot.Scope.RuntimeEpoch != expected {
			failures = append(failures, fmt.Errorf("%s: runtime %q does not match checkpoint runtime %q", filepath.Base(p), cp.Snapshot.Scope.RuntimeEpoch, expected))
			continue
		}
		rep.Checkpoints = append(rep.Checkpoints, cp)
		if cp.Health.Incomplete || cp.Health.LastError != "" || len(cp.Health.Gaps) > 0 {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Checkpoint reports incomplete diagnostic history for "+cp.Snapshot.Scope.RuntimeEpoch)
		}
		if !cp.Sealed {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Runtime is live or ended without a durable seal; later history is unknown")
		}
		if previous[cp.Snapshot.Scope.RuntimeEpoch] < cp.LastSeq {
			rep.Incomplete = true
			rep.Notes = append(rep.Notes, "Snapshot watermark exceeds retained history; the snapshot cannot reconstruct missing events")
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "retention.json")); err == nil {
		rep.Incomplete = true
		rep.Notes = append(rep.Notes, "Retention removed diagnostic history or details; saved results remain in the session")
	}
	if len(files) == 0 && len(checkpoints) == 0 {
		rep.Incomplete = true
		rep.Notes = append(rep.Notes, "Work history unavailable for this session")
	}
	if len(failures) > 0 {
		rep.Incomplete = true
		for _, err := range failures {
			rep.Notes = append(rep.Notes, err.Error())
		}
	}
	return rep, errors.Join(failures...)
}

func ReadDetail(dir, ref string) ([]byte, error) {
	if ref == "" || safe(ref) == "invalid" || filepath.Base(ref) != ref {
		return nil, errors.New("workjournal: invalid detail ref")
	}
	for _, p := range []string{dir, filepath.Join(dir, "details")} {
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid detail directory")
		}
	}
	return readRetentionFile(filepath.Join(dir, "details", ref+".txt"), 64<<10)
}
