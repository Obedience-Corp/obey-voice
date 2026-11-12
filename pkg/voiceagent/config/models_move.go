package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Move phases, in the order a move reports them.
const (
	MovePhaseMeasure = "measuring"
	MovePhaseCopy    = "copying"
	MovePhaseSave    = "saving"
	MovePhaseRemove  = "removing the old copy"
)

// MoveProgress is one progress report from MoveModels. Done and Total are
// bytes and are zero outside the copy phase.
type MoveProgress struct {
	Phase string `json:"phase"`
	Done  int64  `json:"done_bytes"`
	Total int64  `json:"total_bytes"`
}

// MoveResult describes a finished move.
type MoveResult struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// Copied is false when the folder was renamed in place (same volume).
	Copied bool `json:"copied"`
	// RewrittenKeys are other config keys that pointed inside the old folder
	// and now point at the same file in the new one.
	RewrittenKeys []string `json:"rewritten_keys,omitempty"`
	// Warnings are things the move could not finish after the models were
	// already safe in the new folder.
	Warnings []string `json:"warnings,omitempty"`
	// values holds what each rewritten key now says, for ApplyTo.
	values map[string]string
}

// ApplyTo brings an already-loaded Config in line with a finished move: the
// new models folder and every rewritten path. A caller that ran the move on a
// copy of its config uses this to update the one it keeps.
func (r MoveResult) ApplyTo(cfg *Config) {
	if cfg == nil || r.To == "" {
		return
	}
	cfg.ModelsDir = r.To
	for key, value := range r.values {
		setConfigString(cfg, key, value)
	}
}

// setConfigString sets the string field of cfg that the dotted config key
// names, following mapstructure tags. A key with no such field is left alone.
func setConfigString(cfg *Config, key, value string) {
	target := reflect.ValueOf(cfg).Elem()
	segments := strings.Split(key, ".")
	for i, segment := range segments {
		for target.Kind() == reflect.Pointer {
			if target.IsNil() {
				return
			}
			target = target.Elem()
		}
		if target.Kind() != reflect.Struct {
			return
		}
		field := reflect.Value{}
		for j := range target.NumField() {
			if tag, _, _ := strings.Cut(target.Type().Field(j).Tag.Get("mapstructure"), ","); tag == segment {
				field = target.Field(j)
				break
			}
		}
		if !field.IsValid() {
			return
		}
		if i == len(segments)-1 {
			if field.Kind() == reflect.String && field.CanSet() {
				field.SetString(value)
			}
			return
		}
		target = field
	}
}

// MoveOptions carries the optional hooks for MoveModels.
type MoveOptions struct {
	Progress func(MoveProgress)
	// rename is os.Rename outside tests; a test replaces it to force the
	// cross-volume copy path.
	rename func(oldpath, newpath string) error
}

// ModelsUsage reports the bytes and regular files under dir. A missing dir is
// zero, not an error: nothing has been downloaded yet.
func ModelsUsage(ctx context.Context, dir string) (int64, int, error) {
	var total int64
	var files int
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			files++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	return total, files, err
}

// MoveModels moves every model file from the configured models folder to dest
// and then points models_dir at dest.
//
// The old folder is removed only after the new one holds every file and the
// config is saved. A failure or cancellation before that leaves the old folder
// and the config exactly as they were and removes whatever was written to dest.
func MoveModels(ctx context.Context, cfg *Config, dest string, opts MoveOptions) (MoveResult, error) {
	if Source("models_dir") == SourceEnv {
		return MoveResult{}, errors.New("the models folder is set by the MODELS_DIR environment variable; unset it or change it there, then move the files yourself")
	}
	configured := filepath.Clean(expandHomePath(ModelsDirFrom(cfg)))
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return MoveResult{}, errors.New("choose a folder to move the models to")
	}
	dest, err := filepath.Abs(expandHomePath(dest))
	if err != nil {
		return MoveResult{}, fmt.Errorf("resolving %s: %w", dest, err)
	}
	source, err := filepath.EvalSymlinks(configured)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MoveResult{}, fmt.Errorf("there are no models to move: %s does not exist. Set the folder with `obey-voice config set models_dir <folder>` instead", configured)
		}
		return MoveResult{}, fmt.Errorf("resolving %s: %w", configured, err)
	}
	if err := checkMoveTarget(source, configured, dest); err != nil {
		return MoveResult{}, err
	}
	report := opts.Progress
	if report == nil {
		report = func(MoveProgress) {}
	}
	rename := opts.rename
	if rename == nil {
		rename = os.Rename
	}

	report(MoveProgress{Phase: MovePhaseMeasure})
	bytes, files, err := ModelsUsage(ctx, source)
	if err != nil {
		return MoveResult{}, fmt.Errorf("measuring %s: %w", source, err)
	}
	result := MoveResult{From: configured, To: dest, Bytes: bytes, Files: files}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return MoveResult{}, fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}
	// An empty folder the user made in advance is fine; rename needs it gone.
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return MoveResult{}, fmt.Errorf("preparing %s: %w", dest, err)
	}
	if rename(source, dest) != nil {
		result.Copied = true
		if err := copyModelsTree(ctx, source, dest, bytes, report); err != nil {
			_ = os.RemoveAll(dest)
			return MoveResult{}, err
		}
	}
	undo := func() {
		if result.Copied {
			_ = os.RemoveAll(dest)
			return
		}
		_ = rename(dest, source)
	}

	// Last point a cancel is honoured: once the setting is saved the move
	// runs to the end, so it can never stop with the config pointing at a
	// folder that is about to be removed.
	if err := ctx.Err(); err != nil {
		undo()
		return MoveResult{}, err
	}
	report(MoveProgress{Phase: MovePhaseSave})
	if _, err := SetKeyFileValue("models_dir", dest); err != nil {
		undo()
		return MoveResult{}, fmt.Errorf("saving the new models folder: %w", err)
	}
	result.values, result.Warnings = repointModelPaths([]string{configured, source}, dest)
	for key := range result.values {
		result.RewrittenKeys = append(result.RewrittenKeys, key)
	}
	sort.Strings(result.RewrittenKeys)
	result.ApplyTo(cfg)

	if result.Copied {
		report(MoveProgress{Phase: MovePhaseRemove})
		if err := os.RemoveAll(source); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("the old copy at %s could not be removed: %v", source, err))
		}
	}
	// The configured path was a link to the folder that just moved; left in
	// place it would point at nothing.
	if info, err := os.Lstat(configured); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(configured); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("the old link at %s could not be removed: %v", configured, err))
		}
	}
	return result, nil
}

func checkMoveTarget(source, configured, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("reading %s: %w", source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", source)
	}
	resolved := resolveExisting(dest)
	for _, from := range []string{source, configured} {
		if resolved == from || dest == from {
			return fmt.Errorf("the models are already in %s", dest)
		}
		if within(resolved, from) || within(dest, from) {
			return fmt.Errorf("%s is inside the current models folder %s; choose a folder outside it", dest, from)
		}
		if within(from, resolved) {
			return fmt.Errorf("the current models folder %s is inside %s; choose a different folder", from, dest)
		}
	}
	entries, err := os.ReadDir(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%s is not a usable folder: %w", dest, err)
	case len(entries) > 0:
		return fmt.Errorf("%s already has files in it. To use models that are already there, set the folder with `obey-voice config set models_dir %s` instead", dest, dest)
	}
	return nil
}

// resolveExisting resolves symlinks in the longest part of path that exists,
// so a destination that is not created yet still compares correctly against a
// source reached through a link.
func resolveExisting(path string) string {
	rest := ""
	for dir := path; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("reading link %s: %w", path, err)
			}
			return os.Symlink(link, target)
		case !d.Type().IsRegular():
			return nil
		}
		if err := copyModelFile(ctx, path, target, info, func(n int64) {
			done += n
			report(MoveProgress{Phase: MovePhaseCopy, Done: done, Total: total})
		}); err != nil {
			return fmt.Errorf("copying %s: %w", rel, err)
		}
		return nil
	})
}

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
	buf := make([]byte, 4<<20)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			copied += int64(n)
			wrote(int64(n))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if copied != info.Size() {
		return fmt.Errorf("wrote %d of %d bytes", copied, info.Size())
	}
	if err := os.Chmod(to, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(to, info.ModTime(), info.ModTime())
}

// repointModelPaths rewrites every other text setting whose value names a file
// inside one of the old folders, so a path such as whispercpp_model_path keeps
// naming the same file after the move. A value that is only a built-in default
// is written too: those defaults are absolute paths under the default folder.
func repointModelPaths(olds []string, dest string) (rewritten map[string]string, warnings []string) {
	rewritten = map[string]string{}
	for _, spec := range Schema() {
		if spec.Type != TypeString || spec.Key == "models_dir" {
			continue
		}
		mu.RLock()
		value := v.GetString(spec.Key)
		mu.RUnlock()
		clean := filepath.Clean(expandHomePath(value))
		for _, old := range olds {
			if !within(clean, old) {
				continue
			}
			rel, _ := filepath.Rel(old, clean)
			if Source(spec.Key) == SourceEnv {
				warnings = append(warnings, fmt.Sprintf("%s is set in the environment and still points at %s", spec.Key, value))
				break
			}
			moved := filepath.Join(dest, rel)
			if _, err := SetKeyFileValue(spec.Key, moved); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s still points at %s: %v", spec.Key, value, err))
			} else {
				rewritten[spec.Key] = moved
			}
			break
		}
	}
	return rewritten, warnings
}

// FormatBytes renders a byte count with a binary unit suffix.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
