package workjournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var retentionMu sync.Mutex

type retentionStorage struct {
	*storage
	checked    bool
	cleanupErr error
}

func (s *retentionStorage) Write(records []Record, cp Checkpoint) (Health, error) {
	if !s.checked {
		s.checked = true
		s.cleanupErr = PruneSealed(filepath.Dir(s.dir), s.dir, s.opts, time.Now())
	}
	if s.cleanupErr != nil {
		cp.Health.Incomplete = true
		cp.Health.Gaps = append(cp.Health.Gaps, Gap{RuntimeID: s.scope.RuntimeEpoch, Reason: "retention_cleanup_failed"})
	}
	return s.storage.Write(records, cp)
}

type RetentionNotice struct {
	Schema string    `json:"schema"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
	Gaps   []Gap     `json:"gaps"`
}

// PruneSealed removes diagnostics only from closed sidecars. A retired
// conversation checkpoint is insufficient: /clear can reuse that runtime.
// Unknown schemas, unsealed runtimes and the caller's active directory are
// preserved. Normal session files and diagnostic checkpoints are never deleted.
func PruneSealed(root, active string, opts Options, now time.Time) error {
	retentionMu.Lock()
	defer retentionMu.Unlock()
	opts = opts.defaults()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type candidate struct {
		dir   string
		at    time.Time
		size  int64
		paths []string
		gaps  []Gap
	}
	var candidates []candidate
	var total int64
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".work") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if filepath.Clean(dir) == filepath.Clean(active) {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, "snapshot-*.json"))
		if err != nil {
			return err
		}
		if len(files) == 0 {
			continue
		}
		c := candidate{dir: dir}
		sealed := true
		for _, p := range files {
			b, e := readRetentionFile(p, 32<<20)
			if e != nil {
				sealed = false
				break
			}
			var cp Checkpoint
			if json.Unmarshal(b, &cp) != nil || cp.Schema != Schema || !cp.Sealed {
				sealed = false
				break
			}
			if cp.At.After(c.at) {
				c.at = cp.At
			}
		}
		if !sealed {
			continue
		}
		segments, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
		for _, p := range segments {
			st, e := os.Lstat(p)
			if e != nil || !st.Mode().IsRegular() {
				sealed = false
				break
			}
			rs, _, e := readJSONL(p)
			if e != nil {
				sealed = false
				break
			}
			if len(rs) > 0 {
				c.gaps = append(c.gaps, Gap{RuntimeID: rs[0].RuntimeID, From: rs[0].Seq, To: rs[len(rs)-1].Seq, Reason: "sealed_retention"})
			}
			c.paths = append(c.paths, p)
			c.size += st.Size()
		}
		if !sealed {
			continue
		}
		detailDir := filepath.Join(dir, "details")
		if st, e := os.Lstat(detailDir); e == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
			details, _ := os.ReadDir(detailDir)
			for _, d := range details {
				if st, e := d.Info(); e == nil && st.Mode().IsRegular() {
					c.paths = append(c.paths, filepath.Join(detailDir, d.Name()))
					c.size += st.Size()
				}
			}
		}
		if c.size > 0 {
			candidates = append(candidates, c)
			total += c.size
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.Before(candidates[j].at) })
	for _, c := range candidates {
		if now.Sub(c.at) < opts.ExpireAfter && total <= opts.GlobalSealedBytes {
			continue
		}
		notice := RetentionNotice{Schema: Schema, At: now.UTC(), Reason: "sealed diagnostic retention", Gaps: c.gaps}
		// Persist the notice before deletion. A crash may conservatively mark
		// retained files incomplete, but can never silently hide removed ranges.
		if err := saveRetentionNotice(c.dir, notice); err != nil {
			return err
		}
		for _, p := range c.paths {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		total -= c.size
	}
	return nil
}

func readRetentionFile(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, fmt.Errorf("invalid diagnostic file %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func saveRetentionNotice(dir string, n RetentionNotice) error {
	path := filepath.Join(dir, "retention.json")
	if b, err := readRetentionFile(path, 1<<20); err == nil {
		var old RetentionNotice
		if json.Unmarshal(b, &old) != nil || old.Schema != Schema {
			return fmt.Errorf("unsupported retention notice")
		}
		n.Gaps = append(old.Gaps, n.Gaps...)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var compact []Gap
	for _, g := range n.Gaps {
		merged := false
		for i := range compact {
			if compact[i].RuntimeID == g.RuntimeID && compact[i].Reason == g.Reason {
				if g.From < compact[i].From {
					compact[i].From = g.From
				}
				if g.To > compact[i].To {
					compact[i].To = g.To
				}
				merged = true
				break
			}
		}
		if !merged {
			compact = append(compact, g)
		}
	}
	if len(compact) > 128 {
		compact = compact[len(compact)-128:]
		n.Reason = "Older retention ranges omitted; diagnostic history incomplete"
	}
	n.Gaps = compact
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".retention-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
