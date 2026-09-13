package workjournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

var ErrUnsupportedSchema = errors.New("workjournal: unsupported schema")

type storageIO struct {
	open   func(string, int, os.FileMode) (*os.File, error)
	write  func(*os.File, []byte) (int, error)
	sync   func(*os.File) error
	rename func(string, string) error
}

type storage struct {
	dir         string
	scope       work.WorkScope
	opts        Options
	health      Health
	io          storageIO
	initialized bool
	segment     int
	segmentSize int64
	dirty       map[string]bool
	poisoned    error
}

func newStorage(dir string, scope work.WorkScope, opts Options) *storage {
	return &storage{dir: dir, scope: scope, opts: opts.defaults(), io: storageIO{open: os.OpenFile, write: func(f *os.File, b []byte) (int, error) { return f.Write(b) }, sync: func(f *os.File) error { return f.Sync() }, rename: os.Rename}}
}
func (s *storage) ensure() error {
	if !s.scope.Valid() || safe(s.scope.RuntimeEpoch) == "invalid" || safe(s.scope.RuntimeEpoch) != s.scope.RuntimeEpoch {
		return errors.New("workjournal: invalid scope")
	}
	if st, e := os.Lstat(s.dir); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workjournal: storage directory is symlink")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if s.poisoned != nil {
		return s.poisoned
	}
	if s.initialized {
		return nil
	}
	checkpoint := filepath.Join(s.dir, "snapshot-"+safe(s.scope.RuntimeEpoch)+".json")
	if b, err := readRetentionFile(checkpoint, 32<<20); err == nil {
		var cp Checkpoint
		if err = json.Unmarshal(b, &cp); err != nil {
			return err
		}
		if cp.Schema != Schema {
			return fmt.Errorf("%w %q", ErrUnsupportedSchema, cp.Schema)
		}
		if cp.Snapshot.Scope.RuntimeEpoch != s.scope.RuntimeEpoch {
			return errors.New("workjournal: checkpoint runtime differs from storage scope")
		}
		if cp.Sealed {
			return errors.New("workjournal: runtime already sealed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(s.dir, "events-"+safe(s.scope.RuntimeEpoch)+"-*.jsonl"))
	sort.Strings(files)
	s.segment = 1
	for _, path := range files {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return errors.New("invalid event segment")
		}
		_, torn, err := readJSONLForRuntime(path, s.scope.RuntimeEpoch)
		if err != nil {
			return err
		}
		if torn {
			if path != files[len(files)-1] {
				return errors.New("torn nonfinal event segment")
			}
			if err := repairTail(path, st.Size()); err != nil {
				return err
			}
			s.health.Incomplete = true
			s.health.Gaps = append(s.health.Gaps, Gap{RuntimeID: s.scope.RuntimeEpoch, Reason: "torn_tail_unknown_range"})
		}
	}
	if len(files) > 0 {
		last := files[len(files)-1]
		if _, err := fmt.Sscanf(filepath.Base(last), "events-"+safe(s.scope.RuntimeEpoch)+"-%d.jsonl", &s.segment); err != nil {
			return err
		}
		st, err := os.Stat(last)
		if err != nil {
			return err
		}
		s.segmentSize = st.Size()
	}
	s.dirty = map[string]bool{}
	s.initialized = true
	return nil
}
func (s *storage) eventPath(n int) string {
	return filepath.Join(s.dir, fmt.Sprintf("events-%s-%06d.jsonl", safe(s.scope.RuntimeEpoch), n))
}
func safe(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "." || v == ".." || strings.ContainsAny(v, `/\\`) {
		return "invalid"
	}
	return v
}

func (s *storage) Write(records []Record, checkpoint Checkpoint) (Health, error) {
	if err := s.ensure(); err != nil {
		return s.health, err
	}
	if runtime := checkpoint.Snapshot.Scope.RuntimeEpoch; runtime != "" && runtime != s.scope.RuntimeEpoch {
		return s.health, fmt.Errorf("workjournal: checkpoint runtime %q does not match storage runtime %q", runtime, s.scope.RuntimeEpoch)
	}
	for _, r := range records {
		if r.Schema != "" && r.Schema != Schema {
			return s.health, fmt.Errorf("%w %q", ErrUnsupportedSchema, r.Schema)
		}
		if r.SessionID != "" && r.SessionID != s.scope.SessionID {
			return s.health, errors.New("workjournal: session scope mismatch")
		}
		if r.RuntimeID != "" && r.RuntimeID != s.scope.RuntimeEpoch {
			return s.health, errors.New("workjournal: runtime scope mismatch")
		}
		if r.Schema == "" {
			r.Schema = Schema
		}
		if r.SessionID == "" {
			r.SessionID = s.scope.SessionID
		}
		if r.RuntimeID == "" {
			r.RuntimeID = s.scope.RuntimeEpoch
		}
		if r.DetailPayload != nil {
			refBytes := sha256.Sum256([]byte(r.RuntimeID + "/" + fmt.Sprint(r.Seq) + "/" + r.CallID))
			detail := Detail{Ref: hex.EncodeToString(refBytes[:]), OriginalBytes: r.DetailPayload.OriginalBytes}
			if stored, e := s.Detail(detail.Ref, r.DetailPayload.Text, detail.OriginalBytes); e != nil {
				detail.Unavailable = "storage_error"
			} else {
				detail = stored
				if detail.Unavailable != "" {
					detail.Unavailable = "retention_limit"
					detail.Ref = ""
				}
			}
			var obj map[string]json.RawMessage
			if len(r.Data) > 0 && json.Unmarshal(r.Data, &obj) == nil && obj != nil {
				db, _ := json.Marshal(detail)
				obj["details"] = db
				r.Data, _ = json.Marshal(obj)
			}
		}
		b, e := json.Marshal(r)
		if e != nil {
			return s.health, e
		}
		b = append(b, '\n')
		if err := s.append(b, r.Seq, false); err != nil {
			s.health.Incomplete = true
			s.health.LastError = err.Error()
			return s.health, err
		}
	}
	if err := s.syncSegments(); err != nil {
		s.health.Incomplete = true
		s.health.LastError = err.Error()
		return s.health, err
	}
	if checkpoint.Schema == "" {
		checkpoint.Schema = Schema
	}
	if checkpoint.Authority == "" {
		checkpoint.Authority = "diagnostic_only"
	}
	if err := s.retain(); err != nil {
		return s.health, err
	}
	// Preserve queue overflow and retention evidence supplied by the caller.
	merged := checkpoint.Health
	merged.LastError = ""
	// Caller retention failures remain visible; recovered storage errors clear.
	s.health.LastError = ""
	if s.health.Incomplete {
		merged.Incomplete = true
	}
	merged.Dropped += s.health.Dropped
	merged.Gaps = append(append([]Gap(nil), s.health.Gaps...), merged.Gaps...)

	merged.RetainedFrom = s.health.RetainedFrom
	checkpoint.At = time.Now().UTC()
	merged.PersistedSeq = checkpoint.LastSeq
	merged.PersistedAt = checkpoint.At
	checkpoint.Health = merged
	if err := s.saveCheckpoint(checkpoint); err != nil {
		s.health.Incomplete = true
		s.health.LastError = err.Error()
		return s.health, err
	}
	s.health.PersistedSeq = checkpoint.LastSeq
	s.health.PersistedAt = checkpoint.At
	return merged, nil
}
func (s *storage) append(b []byte, _ uint64, _ bool) error {
	if s.segmentSize > 0 && s.segmentSize+int64(len(b)) > s.opts.SegmentBytes {
		s.segment++
		s.segmentSize = 0
	}
	path := s.eventPath(s.segment)
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return errors.New("invalid event segment")
	}
	f, err := s.io.open(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	prior := s.segmentSize
	written, err := s.io.write(f, b)
	if err == nil && written != len(b) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if rollbackErr := f.Truncate(prior); rollbackErr != nil {
			s.poisoned = errors.Join(err, rollbackErr)
		}
		_ = f.Close()
		return errors.Join(err, s.poisoned)
	}
	s.segmentSize += int64(written)
	s.dirty[path] = true
	return f.Close()
}

func (s *storage) syncSegments() error {
	var paths []string
	for p := range s.dirty {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		f, err := s.io.open(path, os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = s.io.sync(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	clear(s.dirty)
	return nil
}

// repairTail removes only an incomplete final line. A malformed complete line
// is left untouched and reported as corruption by the reader.
func repairTail(path string, size int64) error {
	if size == 0 {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.HasSuffix(b, []byte{'\n'}) {
		return nil
	}
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return os.Truncate(path, 0)
	}
	// A record without a terminating newline was never committed by this writer.
	return os.Truncate(path, int64(i+1))
}

func (s *storage) retain() error {
	files, _ := filepath.Glob(filepath.Join(s.dir, "events-*.jsonl"))
	sort.Slice(files, func(i, j int) bool {
		a, ae := os.Stat(files[i])
		b, be := os.Stat(files[j])
		if ae != nil || be != nil {
			return files[i] < files[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	var total int64
	for _, p := range files {
		if st, e := os.Stat(p); e == nil {
			total += st.Size()
		}
	}
	for len(files) > 0 && total > s.opts.SessionBytes {
		if files[0] == s.eventPath(s.segment) {
			files = files[1:]
			continue
		}
		st, e := os.Stat(files[0])
		if e != nil {
			files = files[1:]
			continue
		}
		// Capture the interval before deleting the segment.
		expected, _ := segmentRuntime(files[0])
		rs, _, re := readJSONLForRuntime(files[0], expected)
		if re != nil {
			return re
		}
		if len(rs) > 0 && rs[0].RuntimeID != s.scope.RuntimeEpoch {
			b, err := readRetentionFile(filepath.Join(s.dir, "snapshot-"+safe(rs[0].RuntimeID)+".json"), 32<<20)
			var cp Checkpoint
			if err != nil || json.Unmarshal(b, &cp) != nil || cp.Schema != Schema || !cp.Sealed {
				files = files[1:]
				continue
			}
		}
		if len(rs) > 0 {
			gap := Gap{RuntimeID: rs[0].RuntimeID, From: rs[0].Seq, To: rs[len(rs)-1].Seq, Reason: "retention_prune"}
			if err := saveRetentionNotice(s.dir, RetentionNotice{Schema: Schema, At: time.Now().UTC(), Reason: "session diagnostic retention", Gaps: []Gap{gap}}); err != nil {
				return err
			}
		}
		if e = os.Remove(files[0]); e != nil {
			return e
		}
		if re == nil && len(rs) > 0 {
			s.addRetentionGap(Gap{RuntimeID: rs[0].RuntimeID, From: rs[0].Seq, To: rs[len(rs)-1].Seq, Reason: "retention_prune"})
		}
		total -= st.Size()
		s.health.Incomplete = true
		files = files[1:]
	}
	own, _ := filepath.Glob(filepath.Join(s.dir, "events-"+safe(s.scope.RuntimeEpoch)+"-*.jsonl"))
	sort.Strings(own)
	if len(own) > 0 {
		rs, _, _ := readJSONLForRuntime(own[0], s.scope.RuntimeEpoch)
		if len(rs) > 0 {
			s.health.RetainedFrom = rs[0].Seq
		}
	}
	return nil
}

func (s *storage) addRetentionGap(g Gap) {
	for i := range s.health.Gaps {
		old := &s.health.Gaps[i]
		if old.RuntimeID == g.RuntimeID && old.Reason == g.Reason {
			if old.From == 0 || g.From < old.From {
				old.From = g.From
			}
			if g.To > old.To {
				old.To = g.To
			}
			return
		}
	}
	if len(s.health.Gaps) >= 128 {
		s.health.Gaps = s.health.Gaps[1:]
		s.health.Gaps[0].Reason = "retention_ranges_summarized"
	}
	s.health.Gaps = append(s.health.Gaps, g)
}

func (s *storage) saveCheckpoint(c Checkpoint) error {
	p := filepath.Join(s.dir, "snapshot-"+safe(s.scope.RuntimeEpoch)+".json")
	if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workjournal: checkpoint is symlink")
	}
	if old, e := os.ReadFile(p); e == nil {
		var prior Checkpoint
		if e = json.Unmarshal(old, &prior); e != nil {
			return fmt.Errorf("existing checkpoint: %w", e)
		}
		if prior.Schema != "" && prior.Schema != Schema {
			return fmt.Errorf("%w %q", ErrUnsupportedSchema, prior.Schema)
		}
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.dir, ".snapshot-"+safe(s.scope.RuntimeEpoch)+"-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	var written int
	if written, e = s.io.write(f, append(b, '\n')); e == nil && written != len(b)+1 {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = s.io.sync(f)
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = s.io.rename(tmp, p); e != nil {
		return e
	}
	d, e := s.io.open(s.dir, os.O_RDONLY, 0700)
	if e == nil {
		e = s.io.sync(d)
		_ = d.Close()
	}
	return e
}
func (s *storage) Detail(id string, redacted string, originalBytes int64) (Detail, error) {
	if err := s.ensure(); err != nil {
		return Detail{}, err
	}
	if id == "" || safe(id) == "invalid" {
		return Detail{}, errors.New("workjournal: invalid detail ref")
	}
	b := []byte(redacted)
	d := Detail{Ref: id, OriginalBytes: originalBytes, RetainedBytes: len(b), Truncated: int64(len(b)) < originalBytes}
	if int64(len(b)) > int64(s.opts.DetailBytes) {
		limit := s.opts.DetailBytes
		for limit > 0 && limit < len(b) && (b[limit]&0xc0) == 0x80 {
			limit--
		}
		b = b[:limit]
		d.RetainedBytes = len(b)
		d.Truncated = true
	}
	p := filepath.Join(s.dir, "details")
	if e := os.MkdirAll(p, 0700); e != nil {
		return Detail{}, e
	}
	path := filepath.Join(p, safe(id)+".txt")
	if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return Detail{}, errors.New("workjournal: detail directory is symlink")
	}
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return Detail{}, errors.New("workjournal: detail ref is symlink")
	}
	if existing, err := readRetentionFile(path, int64(s.opts.DetailBytes)); err == nil {
		if bytes.Equal(existing, b) {
			return d, nil
		}
		return Detail{}, errors.New("conflicting detail reference")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Detail{}, err
	}
	if s.opts.SessionDetailBytes > 0 {
		var total int64
		if fs, _ := filepath.Glob(filepath.Join(p, "*.txt")); fs != nil {
			for _, q := range fs {
				if st, e := os.Stat(q); e == nil {
					total += st.Size()
				}
			}
		}
		if total+int64(len(b)) > s.opts.SessionDetailBytes {
			d.Unavailable = "session detail budget exceeded"
			d.RetainedBytes = 0
			return d, nil
		}
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		if os.IsExist(e) {
			existing, re := os.ReadFile(path)
			if re == nil && bytes.Equal(existing, b) {
				return d, nil
			}
		}
		return Detail{}, e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(path)
		return Detail{}, e
	}
	dir, err := os.Open(p)
	if err != nil {
		return Detail{}, err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return Detail{}, err
	}
	if closeErr != nil {
		return Detail{}, closeErr
	}
	return d, nil
}
func (s *storage) Close() error { return nil }
