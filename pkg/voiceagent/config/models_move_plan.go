package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// moveIncompleteMarker sits in a destination while a copy is unfinished, so a
// copy left behind by a killed move is never mistaken for a models folder.
const moveIncompleteMarker = ".obey-voice-move-incomplete"

// moveHeadroom is free space a copy needs beyond the bytes it writes.
const moveHeadroom = 64 << 20

// standardFolders hold more than models; a models_dir that is one of them is
// never moved wholesale.
var standardFolders = []string{
	"Desktop", "Documents", "Downloads", "Library", "Movies", "Music", "Pictures", "Public", "Applications",
	".cache", ".config", ".local", ".obey",
}

// MoveEntry is one top-level entry of the models folder and its size.
type MoveEntry struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// MovePlan is what MoveModels would do. A move with any Refusals does not start.
type MovePlan struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Source and Dest are From and To with links resolved: where the files
	// are read from and where they land.
	Source      string      `json:"source,omitempty"`
	Dest        string      `json:"dest,omitempty"`
	Bytes       int64       `json:"bytes"`
	Files       int         `json:"files"`
	Entries     []MoveEntry `json:"entries,omitempty"`
	SameVolume  bool        `json:"same_volume"`
	FreeBytes   int64       `json:"free_bytes,omitempty"`
	RewriteKeys []string    `json:"rewrite_keys,omitempty"`
	Refusals    []string    `json:"refusals,omitempty"`

	rewrites    []keyValue
	destExisted bool
	destPerm    fs.FileMode
}

// MoveRefusedError is a move that did not start; nothing was changed.
type MoveRefusedError struct{ Reasons []string }

func (e *MoveRefusedError) Error() string { return strings.Join(e.Reasons, "; ") }

// PlanModelsMove checks a move of the models folder to dest and reports what
// it would do, without changing anything. An error means the check itself
// failed; reasons the move would be refused are in Refusals.
func PlanModelsMove(ctx context.Context, cfg *Config, dest string) (MovePlan, error) {
	plan, release, err := planModelsMove(ctx, cfg, dest)
	release()
	return plan, err
}

// planModelsMove is PlanModelsMove that keeps the move's locks until release.
func planModelsMove(ctx context.Context, cfg *Config, to string) (MovePlan, func(), error) {
	var plan MovePlan
	noop := func() {}
	refuse := func(format string, args ...any) (MovePlan, func(), error) {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf(format, args...))
		return plan, noop, nil
	}
	if Source("models_dir") == SourceEnv {
		return refuse("the models folder is set by the MODELS_DIR environment variable; unset it or change it there, then move the files yourself")
	}
	configured := expandHomePath(strings.TrimSpace(ModelsDirFrom(cfg)))
	if !filepath.IsAbs(configured) {
		return refuse("models_dir is a relative path (%s); set it to a full path with `obey-voice config set models_dir <folder>` first", configured)
	}
	plan.From = filepath.Clean(configured)
	to = expandHomePath(strings.TrimSpace(to))
	if to == "" {
		return refuse("choose a folder to move the models to")
	}
	if !filepath.IsAbs(to) {
		return refuse("%s is not a full path; give the whole path, such as /Volumes/Big/models", to)
	}
	plan.To = filepath.Clean(to)

	source, err := filepath.EvalSymlinks(plan.From)
	if errors.Is(err, fs.ErrNotExist) {
		return refuse("there are no models to move: %s does not exist. Set the folder with `obey-voice config set models_dir <folder>` instead", plan.From)
	}
	if err != nil {
		return plan, noop, fmt.Errorf("resolving %s: %w", plan.From, err)
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return refuse("%s is not a folder", plan.From)
	}
	plan.Source = source
	if reason := protectedModelsFolder(plan.From, source); reason != "" {
		return refuse("%s", reason)
	}
	dest, reason := resolveMoveDest(plan.To)
	if reason != "" {
		return refuse("%s", reason)
	}
	plan.Dest = dest
	switch {
	case samePath(source, dest):
		return refuse("the models are already in %s", plan.To)
	case containsPath(source, dest):
		return refuse("%s is inside the current models folder %s; choose a folder outside it", plan.To, plan.From)
	case containsPath(dest, source):
		return refuse("the current models folder %s is inside %s; choose a different folder", plan.From, plan.To)
	}
	if reason := plan.checkDestination(); reason != "" {
		return refuse("%s", reason)
	}

	// The remaining checks are independent; report every one that applies.
	rewrites, envKeys := modelPathRewrites([]string{plan.From, source}, plan.To)
	plan.Refusals = append(plan.Refusals, envKeys...)
	plan.rewrites = rewrites
	for _, kv := range rewrites {
		plan.RewriteKeys = append(plan.RewriteKeys, kv.Key)
	}
	pairs := append([]keyValue{{Key: "models_dir", Value: plan.To}}, rewrites...)
	if err := setKeyFileValues(pairs, true); err != nil {
		plan.Refusals = append(plan.Refusals, describeSaveError(err))
	}

	scan, err := scanModelsTree(ctx, plan.From, source)
	if err != nil {
		return plan, noop, err
	}
	plan.Bytes, plan.Files, plan.Entries = scan.bytes, scan.files, scan.entries
	if len(scan.special) > 0 {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf("%s cannot be copied (not a file, folder or link); move or delete it first", listPaths(scan.special)))
	}
	if len(scan.strayLinks) > 0 {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf("these links would point at the wrong place after the move: %s; replace or remove them first", listPaths(scan.strayLinks)))
	}

	existing := existingAncestor(dest)
	sourceDev, ok1 := deviceOf(source)
	destDev, ok2 := deviceOf(existing)
	plan.SameVolume = ok1 && ok2 && sourceDev == destDev
	if !plan.SameVolume {
		if reason := checkFreeSpace(existing, plan.Bytes, &plan.FreeBytes); reason != "" {
			plan.Refusals = append(plan.Refusals, reason)
		}
	}

	release, lockRefusals := moveLocks(source)
	plan.Refusals = append(plan.Refusals, lockRefusals...)
	return plan, release, nil
}

// checkFreeSpace refuses a copy that would not fit on the volume holding dir.
func checkFreeSpace(dir string, need int64, free *int64) string {
	avail, err := moveHooks.freeSpace(dir)
	if err != nil {
		return ""
	}
	*free = avail
	if avail < need+moveHeadroom {
		return fmt.Sprintf("not enough space: the models need %s and the destination drive has %s free", FormatBytes(need), FormatBytes(avail))
	}
	return ""
}

// protectedModelsFolder refuses a models folder whose wholesale move would take
// far more than models with it.
func protectedModelsFolder(configured, source string) string {
	home := homeDir()
	cfgDir := ConfigDir()
	for _, p := range []string{configured, source} {
		if filepath.IsAbs(home) && containsPath(p, home) {
			return fmt.Sprintf("%s is your home folder or contains it; obey-voice only moves a folder that holds models. Set models_dir to a folder of its own first", configured)
		}
		for _, name := range standardFolders {
			if filepath.IsAbs(home) && samePath(p, filepath.Join(home, name)) {
				return fmt.Sprintf("%s is your %s folder, which holds more than models; set models_dir to a folder of its own first", configured, name)
			}
		}
		if containsPath(p, cfgDir) {
			return fmt.Sprintf("%s holds the obey-voice settings folder %s; moving it would take your settings and personas with it. Set models_dir to a folder of its own first", configured, cfgDir)
		}
	}
	if moveHooks.isMountPoint(source) {
		return fmt.Sprintf("%s is the top of a drive, which obey-voice will not move or delete; copy the models yourself and set models_dir to the new folder", configured)
	}
	return ""
}

// resolveMoveDest returns where the files will land: a link given as the
// destination is followed, never replaced.
func resolveMoveDest(to string) (string, string) {
	if info, err := os.Lstat(to); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(to)
		if err != nil {
			return "", fmt.Sprintf("%s is a link to a folder that does not exist; create that folder or choose another destination", to)
		}
		return target, ""
	}
	return resolveExisting(to), ""
}

func (p *MovePlan) checkDestination() string {
	info, err := os.Lstat(p.Dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ""
	case err != nil:
		return fmt.Sprintf("%s is not a usable folder: %v", p.To, err)
	case !info.IsDir():
		return fmt.Sprintf("%s is a file, not a folder", p.To)
	}
	entries, err := os.ReadDir(p.Dest)
	if err != nil {
		return fmt.Sprintf("%s is not a usable folder: %v", p.To, err)
	}
	if len(entries) > 0 {
		if _, err := os.Lstat(filepath.Join(p.Dest, moveIncompleteMarker)); err == nil {
			return fmt.Sprintf("%s holds an unfinished copy from an earlier move that was stopped, and nothing uses it; delete that folder, then try again", p.To)
		}
		return fmt.Sprintf("%s already has files in it. To use models that are already there, set the folder with `obey-voice config set models_dir %s` instead", p.To, p.To)
	}
	p.destExisted, p.destPerm = true, info.Mode().Perm()
	return ""
}

// modelPathRewrites lists every other text setting that names a file inside
// one of the old folders, with the same file in the new one. A setting that
// comes from the environment cannot be rewritten and is returned as a refusal.
func modelPathRewrites(olds []string, to string) (rewrites []keyValue, envRefusals []string) {
	for _, spec := range Schema() {
		if spec.Type != TypeString || spec.Key == "models_dir" {
			continue
		}
		mu.RLock()
		value := v.GetString(spec.Key)
		mu.RUnlock()
		if strings.TrimSpace(value) == "" {
			continue
		}
		clean := filepath.Clean(expandHomePath(value))
		for _, old := range olds {
			if !within(clean, old) {
				continue
			}
			if name, _, ok := envValueFor(spec.Key); ok {
				envRefusals = append(envRefusals, fmt.Sprintf("%s (%s) is set in the environment to %s, inside the models folder, and cannot be updated; unset it or point it at the new folder, then try again", name, spec.Key, value))
				break
			}
			rel, _ := filepath.Rel(old, clean)
			rewrites = append(rewrites, keyValue{Key: spec.Key, Value: filepath.Join(to, rel)})
			break
		}
	}
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].Key < rewrites[j].Key })
	return rewrites, envRefusals
}

func describeSaveError(err error) string {
	var setErr *SetError
	if errors.As(err, &setErr) && setErr.Key != "" {
		return fmt.Sprintf("the setting %s cannot be saved to %s: %s", setErr.Key, ConfigFile(), setErr.Message)
	}
	return fmt.Sprintf("the new folder cannot be saved to %s: %v", ConfigFile(), err)
}

type modelsScan struct {
	bytes      int64
	files      int
	entries    []MoveEntry
	special    []string
	strayLinks []string
}

// scanModelsTree measures the folder and finds what a move could not carry
// over faithfully: special files, and links that would point somewhere else
// once the folder has moved.
func scanModelsTree(ctx context.Context, configured, source string) (modelsScan, error) {
	var scan modelsScan
	sizes := map[string]int64{}
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		if _, ok := sizes[top]; !ok {
			sizes[top] = 0
		}
		switch {
		case d.IsDir():
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("reading link %s: %w", path, err)
			}
			if linkBreaksOnMove(rel, target, configured, source) {
				scan.strayLinks = append(scan.strayLinks, rel+" → "+target)
			}
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}
			scan.bytes += info.Size()
			scan.files++
			sizes[top] += info.Size()
		default:
			scan.special = append(scan.special, rel)
		}
		return nil
	})
	if err != nil {
		return scan, err
	}
	for name, size := range sizes {
		scan.entries = append(scan.entries, MoveEntry{Name: name, Bytes: size})
	}
	sort.Slice(scan.entries, func(i, j int) bool { return scan.entries[i].Name < scan.entries[j].Name })
	return scan, nil
}

// linkBreaksOnMove reports a link that names its target through the old
// folder's path (absolute into it, or relative out of it), so it would point
// somewhere else after the move.
func linkBreaksOnMove(rel, target, configured, source string) bool {
	if filepath.IsAbs(target) {
		clean := filepath.Clean(target)
		for _, old := range []string{configured, source} {
			if clean == old || within(clean, old) {
				return true
			}
		}
		return false
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(rel), target))
	return resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator))
}

func listPaths(paths []string) string {
	const shown = 5
	if len(paths) <= shown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:shown], ", "), len(paths)-shown)
}

// containsPath reports whether dir is path or one of its ancestors. Existing
// folders are compared by identity, so a case variant or a link on the way is
// seen through.
func containsPath(dir, path string) bool {
	dirInfo, err := os.Stat(dir)
	if err != nil {
		clean := filepath.Clean(path)
		return clean == filepath.Clean(dir) || within(clean, filepath.Clean(dir))
	}
	for p := filepath.Clean(path); ; {
		if info, err := os.Stat(p); err == nil && os.SameFile(info, dirInfo) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

func samePath(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func existingAncestor(path string) string {
	for p := filepath.Clean(path); ; {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
