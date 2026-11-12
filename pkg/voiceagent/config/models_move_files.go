package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// moveHookSet holds what tests replace to force or observe a path through a
// move. Production uses the real calls.
type moveHookSet struct {
	rename       func(oldpath, newpath string) error
	symlink      func(oldname, newname string) error
	freeSpace    func(path string) (int64, error)
	isMountPoint func(path string) bool
	deviceOf     func(path string) (uint64, bool)
	homeFallback func() string
	// syncFile flushes a copied file; syncDir flushes a folder's entries;
	// syncConfig flushes config.yaml after the save.
	syncFile   func(f *os.File) error
	syncDir    func(path string) error
	syncConfig func(path string) error
	// writeConfig saves models_dir and the repointed settings.
	writeConfig func(build func(func(string) (string, bool)) []keyValue) ([]keyValue, error)
	// afterCopy runs after each file is copied, with the move's context.
	afterCopy func(ctx context.Context, rel string)
	// afterRename runs after a same-drive rename, before the save.
	afterRename func(ctx context.Context)
	// beforeVerify runs before each comparison of the set-aside old folder.
	beforeVerify func(quarantine string, pass int)
}

var moveHooks = defaultMoveHooks()

func defaultMoveHooks() moveHookSet {
	return moveHookSet{
		rename: os.Rename, symlink: os.Symlink, freeSpace: freeSpace, isMountPoint: isMountPoint,
		deviceOf: deviceOf, homeFallback: userHomeDir,
		syncFile:   func(f *os.File) error { return f.Sync() },
		syncDir:    syncDir,
		syncConfig: syncFile,
		writeConfig: func(build func(func(string) (string, bool)) []keyValue) ([]keyValue, error) {
			return setKeyFileValuesFrom(build, false)
		},
	}
}

// ModelsMoveTestHooks lets another package's tests force the cross-volume
// copy and act in the middle of it. Nil fields keep the real behaviour.
type ModelsMoveTestHooks struct {
	Rename    func(oldpath, newpath string) error
	AfterCopy func(ctx context.Context, rel string)
	// ServeRoot replaces the folder probed for running serve instances.
	ServeRoot string
}

// SetModelsMoveHooksForTest installs h for the duration of t.
func SetModelsMoveHooksForTest(t interface {
	Helper()
	Cleanup(func())
}, h ModelsMoveTestHooks) {
	t.Helper()
	orig, origRoot := moveHooks, serveControlRoot
	if h.Rename != nil {
		moveHooks.rename = h.Rename
	}
	if h.AfterCopy != nil {
		moveHooks.afterCopy = h.AfterCopy
	}
	if h.ServeRoot != "" {
		root := h.ServeRoot
		serveControlRoot = func() string { return root }
	}
	t.Cleanup(func() { moveHooks, serveControlRoot = orig, origRoot })
}

// quarantineReadme is written into the old folder once it is set aside, so a
// folder left behind by a killed or cautious move explains itself.
const quarantineReadme = "README-obey-voice-move.txt"

// fileStamp identifies one state of a file: which file it is, its size, and
// when its contents and metadata last changed (ctime is zero where the
// platform does not report it).
type fileStamp struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
}

// copyRecord is what the move knows about each file it copied: a hash of the
// exact bytes written to the new folder, and the old file's stamp when it was
// last read.
type copyRecord struct {
	hashes map[string][sha256.Size]byte
	stamps map[string]fileStamp
}

func newCopyRecord() *copyRecord {
	return &copyRecord{hashes: map[string][sha256.Size]byte{}, stamps: map[string]fileStamp{}}
}

// makeParents creates the missing folders above dest and returns them,
// deepest first, so a failed move can take away exactly those.
func makeParents(dest string) ([]string, error) {
	var created []string
	for p := filepath.Dir(dest); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil || filepath.Dir(p) == p {
			break
		}
		created = append(created, p)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		removeEmpty(created)
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}
	return created, nil
}

func removeEmpty(dirs []string) {
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}

// destCopy is a destination this move is filling. A failure removes what the
// move wrote there and nothing the user had.
type destCopy struct {
	dir     string
	existed bool
}

func prepareDest(dest string, existed bool) (*destCopy, error) {
	d := &destCopy{dir: dest, existed: existed}
	if !existed {
		if err := os.Mkdir(dest, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dest, err)
		}
	}
	marker := filepath.Join(dest, moveIncompleteMarker)
	if err := os.WriteFile(marker, []byte("obey-voice was copying models here. If no move is running, these files are an unfinished copy and can be deleted.\n"), 0o644); err != nil {
		d.discard()
		return nil, fmt.Errorf("writing to %s: %w", dest, err)
	}
	return d, nil
}

// discard removes what this move wrote: the whole folder when the move made
// it, only its contents when the user had made it empty in advance.
func (d *destCopy) discard() {
	if !d.existed {
		_ = os.RemoveAll(d.dir)
		return
	}
	entries, _ := os.ReadDir(d.dir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(d.dir, e.Name()))
	}
}

// finish marks the copy complete once everything in it is on disk.
func (d *destCopy) finish() error {
	if err := syncTree(d.dir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(d.dir, moveIncompleteMarker)); err != nil {
		return err
	}
	if err := moveHooks.syncDir(d.dir); err != nil {
		return fmt.Errorf("syncing %s: %w", d.dir, err)
	}
	return moveHooks.syncDir(filepath.Dir(d.dir))
}

// skipAtRoot names what the move itself writes into a folder; it is never
// carried between folders.
func skipAtRoot(rel string) bool {
	return rel == moveIncompleteMarker || rel == quarantineReadme
}

// copyModelsTree copies source into dest, recording each file under prefix
// (its path relative to the folder being moved).
func copyModelsTree(ctx context.Context, source, dest, prefix string, total int64, report func(MoveProgress), rec *copyRecord) error {
	var done int64
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if prefix == "" && skipAtRoot(rel) {
			return nil
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		key := filepath.Join(prefix, rel)
		switch {
		case d.IsDir():
			// Owner-writable so it can be filled even when the source
			// folder was read-only.
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			return copyLink(path, target, key)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a file, folder or link and cannot be copied", key)
		}
		if err := copyModelFile(ctx, path, target, key, info, rec, func(n int64) {
			done += n
			report(MoveProgress{Phase: MovePhaseCopy, Done: done, Total: total})
		}); err != nil {
			return fmt.Errorf("copying %s: %w", key, err)
		}
		if moveHooks.afterCopy != nil {
			moveHooks.afterCopy(ctx, key)
		}
		return nil
	})
}

func copyLink(path, target, rel string) error {
	link, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("reading link %s: %w", path, err)
	}
	if err := moveHooks.symlink(link, target); err != nil {
		return fmt.Errorf("the destination drive refused the link %s (exFAT and FAT32 drives cannot hold links; use a drive formatted APFS, HFS+ or ext4): %w", rel, err)
	}
	return nil
}

// copyModelFile copies one file, hashing the bytes it writes, and flushes it
// to disk before it counts as copied.
func copyModelFile(ctx context.Context, from, to, key string, info fs.FileInfo, rec *copyRecord, wrote func(int64)) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	sum, err := copyAndSync(ctx, in, out, wrote)
	if err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	stamp, err := stampOf(in)
	if err != nil {
		return err
	}
	rec.hashes[key], rec.stamps[key] = sum, stamp
	if err := os.Chmod(to, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}

func copyAndSync(ctx context.Context, in io.Reader, out *os.File, wrote func(int64)) ([sha256.Size]byte, error) {
	h := sha256.New()
	buf := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return [sha256.Size]byte{}, err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return [sha256.Size]byte{}, err
			}
			h.Write(buf[:n])
			wrote(int64(n))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return [sha256.Size]byte{}, readErr
		}
	}
	if err := moveHooks.syncFile(out); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// syncTree flushes every directory under root, so the entries naming the
// copied files are on disk too.
func syncTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if err := moveHooks.syncDir(path); err != nil {
			return fmt.Errorf("syncing %s: %w", path, err)
		}
		return nil
	})
}

// quarantineName is a free sibling of dir to set it aside under.
func quarantineName(dir string) string {
	base := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".obey-voice-old-"+time.Now().Format("20060102-150405"))
	name := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func writeQuarantineReadme(old, dest string) error {
	text := fmt.Sprintf("obey-voice moved the models in this folder to\n  %s\non %s.\n\n"+
		"If this folder is still here, the move kept it on purpose or was stopped\n"+
		"before it could check it. It may hold files the new folder lacks: compare\n"+
		"the two before deleting this one.\n", dest, time.Now().Format(time.RFC1123))
	return os.WriteFile(filepath.Join(old, quarantineReadme), []byte(text), 0o644)
}

// reconcile checks the set-aside old folder against what was copied. The
// first pass reads every old file and compares its hash with the hash of the
// bytes written to the new folder, so no change can pass for unchanged; what
// differs is copied over. The second pass only compares each file's stamp
// with the first pass's, to catch a writer still at work. It returns what
// differed on the second pass: the old folder must then be kept.
func reconcile(ctx context.Context, old, dest string, rec *copyRecord, report func(MoveProgress)) ([]string, error) {
	if moveHooks.beforeVerify != nil {
		moveHooks.beforeVerify(old, 1)
	}
	var total, done int64
	for _, s := range rec.stamps {
		total += s.size
	}
	var changed []string
	err := filepath.WalkDir(old, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(old, path)
		if err != nil || rel == "." || skipAtRoot(rel) {
			return err
		}
		same, err := sameAsCopied(path, filepath.Join(dest, rel), rel, d, rec, func(n int64) {
			done += n
			report(MoveProgress{Phase: MovePhaseVerify, Done: min(done, total), Total: total})
		})
		if err != nil {
			return err
		}
		if !same {
			changed = append(changed, rel)
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, rel := range changed {
		if err := copyOver(ctx, filepath.Join(old, rel), filepath.Join(dest, rel), rel, rec); err != nil {
			return nil, fmt.Errorf("copying %s, which changed during the move: %w", rel, err)
		}
	}
	if len(changed) > 0 {
		if err := syncTree(dest); err != nil {
			return nil, err
		}
	}
	if moveHooks.beforeVerify != nil {
		moveHooks.beforeVerify(old, 2)
	}
	return stillChanging(old, dest, rec)
}

// sameAsCopied reports whether one old entry matches what the new folder
// holds. A regular file is read in full and hashed.
func sameAsCopied(oldPath, newPath, rel string, d fs.DirEntry, rec *copyRecord, read func(int64)) (bool, error) {
	got, err := os.Lstat(newPath)
	if err != nil || got.Mode().Type() != d.Type() {
		return false, nil
	}
	switch {
	case d.IsDir():
		return true, nil
	case d.Type()&fs.ModeSymlink != 0:
		a, _ := os.Readlink(oldPath)
		b, _ := os.Readlink(newPath)
		return a == b, nil
	case !d.Type().IsRegular():
		return false, nil
	}
	f, err := os.Open(oldPath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	before, err := stampOf(f)
	if err != nil {
		return false, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, readCounter{f, read}); err != nil {
		return false, err
	}
	after, err := stampOf(f)
	if err != nil {
		return false, err
	}
	want, ok := rec.hashes[rel]
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	rec.stamps[rel] = after
	return ok && before == after && sum == want && got.Size() == after.size, nil
}

type readCounter struct {
	r    io.Reader
	read func(int64)
}

func (c readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read(int64(n))
	return n, err
}

// stillChanging is the second look: anything in the old folder that is new
// since the first pass, or whose stamp differs from the one recorded then.
// A file that has since vanished from the old folder is not a loss; the new
// folder holds its last state.
func stillChanging(old, dest string, rec *copyRecord) ([]string, error) {
	var differ []string
	err := filepath.WalkDir(old, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(old, path)
		if err != nil || rel == "." || skipAtRoot(rel) {
			return err
		}
		got, err := os.Lstat(filepath.Join(dest, rel))
		switch {
		case err != nil || got.Mode().Type() != d.Type():
			differ = append(differ, rel)
		case d.Type()&fs.ModeSymlink != 0:
			a, _ := os.Readlink(path)
			b, _ := os.Readlink(filepath.Join(dest, rel))
			if a != b {
				differ = append(differ, rel)
			}
		case d.Type().IsRegular():
			want, ok := rec.stamps[rel]
			now, err := stampAt(path)
			if !ok || err != nil || now != want {
				differ = append(differ, rel)
			}
		}
		return nil
	})
	return differ, err
}

// copyOver replaces to with a fresh copy of from (a file, link or folder).
func copyOver(ctx context.Context, from, to, rel string, rec *copyRecord) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	// A .part name, so models clean treats a leftover as junk.
	tmp := to + ".obey-voice-move.part"
	_ = os.RemoveAll(tmp)
	switch {
	case info.IsDir():
		err = copyModelsTree(ctx, from, tmp, rel, 0, func(MoveProgress) {}, rec)
	case info.Mode()&fs.ModeSymlink != 0:
		err = copyLink(from, tmp, rel)
	default:
		err = copyModelFile(ctx, from, tmp, rel, info, rec, func(int64) {})
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if existing, err := os.Lstat(to); err == nil && existing.IsDir() {
		if err := os.RemoveAll(to); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	}
	return os.Rename(tmp, to)
}

// mountInside finds a folder under root that is on another drive. Deleting
// root would recurse into that drive.
func mountInside(root string) (string, bool) {
	rootDev, ok := moveHooks.deviceOf(root)
	if !ok {
		return "", false
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		if dev, ok := moveHooks.deviceOf(path); ok && dev != rootDev {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}
