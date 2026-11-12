package config

import (
	"context"
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
	// afterCopy runs after each file is copied, with the move's context.
	afterCopy func(ctx context.Context, rel string)
	// beforeVerify runs before each comparison of the set-aside old folder.
	beforeVerify func(quarantine string, pass int)
	// trace records syncs, the save and removals, in order.
	trace func(event string)
}

var moveHooks = defaultMoveHooks()

func defaultMoveHooks() moveHookSet {
	return moveHookSet{rename: os.Rename, symlink: os.Symlink, freeSpace: freeSpace, isMountPoint: isMountPoint}
}

func traceMove(format string, args ...any) {
	if moveHooks.trace != nil {
		moveHooks.trace(fmt.Sprintf(format, args...))
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
	if err := os.WriteFile(marker, []byte("obey-voice was copying models here. If no move is running, this folder is an unfinished copy and can be deleted.\n"), 0o644); err != nil {
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
	if err := syncDir(d.dir); err != nil {
		return fmt.Errorf("syncing %s: %w", d.dir, err)
	}
	return syncDir(filepath.Dir(d.dir))
}

func copyModelsTree(ctx context.Context, source, dest string, total int64, report func(MoveProgress)) error {
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
		if rel == moveIncompleteMarker {
			return nil
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		switch {
		case d.IsDir():
			// Owner-writable so it can be filled even when the source
			// folder was read-only.
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSymlink != 0:
			return copyLink(path, target, rel)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a file, folder or link and cannot be copied", rel)
		}
		if err := copyModelFile(ctx, path, target, info, func(n int64) {
			done += n
			report(MoveProgress{Phase: MovePhaseCopy, Done: done, Total: total})
		}); err != nil {
			return fmt.Errorf("copying %s: %w", rel, err)
		}
		if moveHooks.afterCopy != nil {
			moveHooks.afterCopy(ctx, rel)
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

// copyModelFile copies one file and flushes it to disk before it counts as
// copied.
func copyModelFile(ctx context.Context, from, to string, info fs.FileInfo, wrote func(int64)) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o200)
	if err != nil {
		return err
	}
	if err := copyAndSync(ctx, in, out, wrote); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	traceMove("sync file %s", to)
	if err := os.Chmod(to, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}

func copyAndSync(ctx context.Context, in io.Reader, out *os.File, wrote func(int64)) error {
	buf := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			wrote(int64(n))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return out.Sync()
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
		if err := syncDir(path); err != nil {
			return fmt.Errorf("syncing %s: %w", path, err)
		}
		traceMove("sync dir %s", path)
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

// reconcile brings into dest anything that changed in the old folder after it
// was copied, then checks again. It returns what still differs on the second
// look: something is still writing there, so the old folder must be kept.
func reconcile(ctx context.Context, old, dest string) ([]string, error) {
	if moveHooks.beforeVerify != nil {
		moveHooks.beforeVerify(old, 1)
	}
	changed, err := compareTrees(old, dest)
	if err != nil {
		return nil, err
	}
	for _, rel := range changed {
		if err := copyOver(ctx, filepath.Join(old, rel), filepath.Join(dest, rel)); err != nil {
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
	return compareTrees(old, dest)
}

// compareTrees lists what in old is missing from dest or differs from it by
// kind, size, modification time or link target.
func compareTrees(old, dest string) ([]string, error) {
	var differ []string
	err := filepath.WalkDir(old, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(old, path)
		if err != nil || rel == "." || rel == moveIncompleteMarker {
			return err
		}
		want, err := os.Lstat(path)
		if err != nil {
			return err
		}
		got, err := os.Lstat(filepath.Join(dest, rel))
		if err != nil || want.Mode().Type() != got.Mode().Type() {
			differ = append(differ, rel)
			if want.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case want.Mode().IsRegular():
			if want.Size() != got.Size() || absDuration(want.ModTime().Sub(got.ModTime())) > 2*time.Second {
				differ = append(differ, rel)
			}
		case want.Mode()&fs.ModeSymlink != 0:
			a, _ := os.Readlink(path)
			b, _ := os.Readlink(filepath.Join(dest, rel))
			if a != b {
				differ = append(differ, rel)
			}
		}
		return nil
	})
	return differ, err
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// copyOver replaces to with a fresh copy of from (a file, link or folder).
func copyOver(ctx context.Context, from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	tmp := to + ".obey-voice-move-tmp"
	_ = os.RemoveAll(tmp)
	switch {
	case info.IsDir():
		if err := copyModelsTree(ctx, from, tmp, 0, func(MoveProgress) {}); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	case info.Mode()&fs.ModeSymlink != 0:
		if err := copyLink(from, tmp, filepath.Base(from)); err != nil {
			return err
		}
	default:
		if err := copyModelFile(ctx, from, tmp, info, func(int64) {}); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if existing, err := os.Lstat(to); err == nil && existing.IsDir() {
		if err := os.RemoveAll(to); err != nil {
			return err
		}
	}
	return os.Rename(tmp, to)
}
