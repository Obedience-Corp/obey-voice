package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
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
	Source string `json:"source,omitempty"`
	Dest   string `json:"dest,omitempty"`
	// Measured is false when the move was refused before the folder was
	// read; Bytes, Files and Entries are then empty.
	Measured bool        `json:"measured"`
	Bytes    int64       `json:"bytes"`
	Files    int         `json:"files"`
	Entries  []MoveEntry `json:"entries,omitempty"`
	// Unrecognized are top-level entries that are not models this install
	// knows. They move along, and a copied old folder holding any of them is
	// kept rather than deleted.
	Unrecognized []MoveEntry `json:"unrecognized,omitempty"`
	SameVolume   bool        `json:"same_volume"`
	FreeBytes    int64       `json:"free_bytes,omitempty"`
	RewriteKeys  []string    `json:"rewrite_keys,omitempty"`
	Refusals     []string    `json:"refusals,omitempty"`

	destExisted bool
	destPerm    fs.FileMode
}

// MoveRefusedError is a move that did not start; nothing was changed.
type MoveRefusedError struct{ Reasons []string }

func (e *MoveRefusedError) Error() string { return strings.Join(e.Reasons, "; ") }

// PlanModelsMove checks a move of the models folder to dest and reports what
// it would do. It writes nothing, not even a lock file. An error means the
// check itself failed; reasons the move would be refused are in Refusals.
func PlanModelsMove(ctx context.Context, cfg *Config, dest string, opts MoveOptions) (MovePlan, error) {
	return planModelsMove(ctx, cfg, dest, opts, false)
}

// planModelsMove is PlanModelsMove; held says the caller already holds the
// move's locks, so they are not probed.
func planModelsMove(ctx context.Context, cfg *Config, to string, opts MoveOptions, held bool) (MovePlan, error) {
	var plan MovePlan
	refuse := func(format string, args ...any) (MovePlan, error) {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf(format, args...))
		return plan, nil
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
		return plan, fmt.Errorf("resolving %s: %w", plan.From, err)
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
	plan.Refusals = append(plan.Refusals, environmentRefusals(source)...)
	pairs, err := setKeyFileValuesFrom(modelPathPairs(plan.From, source, plan.To), true)
	if err != nil {
		plan.Refusals = append(plan.Refusals, describeSaveError(err))
	}
	for _, kv := range pairs[min(1, len(pairs)):] {
		plan.RewriteKeys = append(plan.RewriteKeys, kv.Key)
	}

	scan, err := scanModelsTree(ctx, source)
	if err != nil {
		return plan, err
	}
	plan.Measured = true
	plan.Bytes, plan.Files, plan.Entries = scan.bytes, scan.files, scan.entries
	if len(scan.special) > 0 {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf("%s cannot be copied (not a file, folder or link); move or delete it first", listPaths(scan.special)))
	}
	if len(scan.strayLinks) > 0 {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf("these links would point at the wrong place after the move: %s; replace or remove them first", listPaths(scan.strayLinks)))
	}
	if len(scan.mounts) > 0 {
		plan.Refusals = append(plan.Refusals, fmt.Sprintf("%s is another drive mounted inside the models folder; unmount it first", listPaths(scan.mounts)))
	}
	plan.Unrecognized = unrecognizedEntries(ctx, cfg, plan.From, plan.Entries, opts.Personas)

	existing := existingAncestor(dest)
	sourceDev, ok1 := moveHooks.deviceOf(source)
	destDev, ok2 := moveHooks.deviceOf(existing)
	plan.SameVolume = ok1 && ok2 && sourceDev == destDev
	if !plan.SameVolume {
		if reason := checkFreeSpace(existing, plan.Bytes, &plan.FreeBytes); reason != "" {
			plan.Refusals = append(plan.Refusals, reason)
		}
	}
	if !held {
		plan.Refusals = append(plan.Refusals, probeMoveLocks(source)...)
	}
	return plan, nil
}

// checkFreeSpace refuses a copy that would not fit on the volume holding dir.
// Unknown free space is not a refusal: a full drive fails the copy, which is
// then removed.
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

func userHomeDir() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// protectedModelsFolder refuses a models folder whose wholesale move would take
// far more than models with it.
func protectedModelsFolder(configured, source string) string {
	home := homeDir()
	if !filepath.IsAbs(home) {
		home = moveHooks.homeFallback()
	}
	if !filepath.IsAbs(home) {
		return "obey-voice cannot tell where your home folder is (HOME is not set), so it cannot check that the models folder is safe to move; set HOME and try again"
	}
	cfgDir := ConfigDir()
	for _, p := range []string{configured, source} {
		if containsPath(p, home) {
			return fmt.Sprintf("%s is your home folder or contains it; obey-voice only moves a folder that holds models. Set models_dir to a folder of its own first", configured)
		}
		for _, name := range standardFolders {
			if samePath(p, filepath.Join(home, name)) {
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
		if _, err := filepath.EvalSymlinks(to); err != nil {
			return "", fmt.Sprintf("%s is a link to a folder that does not exist; create that folder or choose another destination", to)
		}
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
		if marker := filepath.Join(p.Dest, moveIncompleteMarker); fileExists(marker) {
			where := p.Dest
			if p.Dest != p.To {
				where = fmt.Sprintf("%s (where %s leads)", p.Dest, p.To)
			}
			return fmt.Sprintf("%s holds an unfinished copy from an earlier move that was stopped (%s is there) and nothing uses those files; delete everything inside %s, then try again", where, marker, p.Dest)
		}
		return fmt.Sprintf("%s already has files in it. To use models that are already there, set the folder with `obey-voice config set models_dir %s` instead", p.To, p.To)
	}
	p.destExisted, p.destPerm = true, info.Mode().Perm()
	return ""
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// environmentRefusals names settings set in the environment that point into
// the old folder: they cannot be rewritten, and would name deleted files.
func environmentRefusals(source string) []string {
	var refusals []string
	for _, spec := range Schema() {
		if spec.Type != TypeString || spec.Key == "models_dir" {
			continue
		}
		if name, raw, ok := envValueFor(spec.Key); ok {
			if _, inside := insideFolder(source, expandHomePath(strings.TrimSpace(raw))); inside {
				refusals = append(refusals, fmt.Sprintf("%s (%s) is set in the environment to %s, inside the models folder, and cannot be updated; unset it or point it at the new folder, then try again", name, spec.Key, raw))
			}
		}
	}
	return refusals
}

// modelPathPairs builds the settings a move saves from what config.yaml says
// when it is read under the write lock: models_dir, and every other text
// setting naming a file inside the old folder, pointed at the same file in
// the new one. A setting config.yaml does not hold is judged by its default.
// Settings set in the environment are refused beforehand, not rewritten.
//
// After a same-drive rename the old folder no longer exists, so a value is
// also matched by its spelling against the old folder's two spellings, as
// given and with links resolved.
func modelPathPairs(from, source, to string) func(func(string) (string, bool)) []KeyWrite {
	return func(fileValue func(string) (string, bool)) []KeyWrite {
		var rewrites []KeyWrite
		for _, spec := range Schema() {
			if spec.Type != TypeString || spec.Key == "models_dir" {
				continue
			}
			if _, _, ok := envValueFor(spec.Key); ok {
				continue
			}
			value, ok := fileValue(spec.Key)
			if !ok {
				value, _ = spec.Default.(string)
			}
			if rel, inside := insideOldFolder(from, source, expandHomePath(strings.TrimSpace(value))); inside {
				rewrites = append(rewrites, KeyWrite{Key: spec.Key, Value: filepath.Join(to, rel)})
			}
		}
		sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].Key < rewrites[j].Key })
		return append([]KeyWrite{{Key: "models_dir", Value: to}}, rewrites...)
	}
}

// insideOldFolder is insideFolder for the folder being moved, which may
// already have been renamed away.
func insideOldFolder(from, source, path string) (string, bool) {
	if rel, ok := insideFolder(source, path); ok {
		return rel, true
	}
	if !filepath.IsAbs(path) {
		return "", false
	}
	path = filepath.Clean(path)
	for _, spelled := range []string{path, resolveExisting(path)} {
		for _, old := range []string{from, source} {
			if spelled == old {
				return ".", true
			}
			if within(spelled, old) {
				rel, err := filepath.Rel(old, spelled)
				return rel, err == nil
			}
		}
	}
	return "", false
}

// insideFolder reports where path lies inside dir ("." when it is dir).
// Folders are compared by identity along both the given spelling and the one
// with links resolved, so a link or case variant anywhere on the way, on
// either side, is seen through. The path itself need not exist.
func insideFolder(dir, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return "", false
	}
	path = filepath.Clean(path)
	for _, spelled := range []string{path, resolveExisting(path)} {
		for p := spelled; ; {
			if info, err := os.Stat(p); err == nil && os.SameFile(info, dirInfo) {
				rel, err := filepath.Rel(p, spelled)
				return rel, err == nil
			}
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	return "", false
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
	mounts     []string
}

// scanModelsTree measures the folder and finds what a move could not carry
// over faithfully: special files, links that would point somewhere else once
// the folder has moved, and other drives mounted inside it.
func scanModelsTree(ctx context.Context, source string) (modelsScan, error) {
	var scan modelsScan
	sizes := map[string]int64{}
	rootDev, rootDevOK := moveHooks.deviceOf(source)
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
			if dev, ok := moveHooks.deviceOf(path); rootDevOK && ok && dev != rootDev {
				scan.mounts = append(scan.mounts, rel)
				return fs.SkipDir
			}
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("reading link %s: %w", path, err)
			}
			if linkBreaksOnMove(rel, target, source) {
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

// unrecognizedEntries lists the top-level entries that are not models this
// install references (through its settings or any persona) and not files the
// installers or the move write themselves. If the models in use cannot be
// worked out, every entry is unrecognized: that only means the old folder is
// kept.
func unrecognizedEntries(ctx context.Context, cfg *Config, modelsDir string, entries []MoveEntry, personas []PersonaAssets) []MoveEntry {
	set, err := RequiredAssetPaths(ctx, cfg, modelsDir, personas)
	var out []MoveEntry
	for _, e := range entries {
		name, path := e.Name, filepath.Join(modelsDir, e.Name)
		switch {
		case skipAtRoot(name), name == deciderLockSubdir, name == ".DS_Store",
			strings.HasPrefix(name, ".samantha-asset-"), cleanCategory(name) == CleanCategoryJunk:
			continue
		case err == nil && (set.own.has(path) || set.own.holdsRequired(path)):
			continue
		}
		out = append(out, e)
	}
	return out
}

// linkBreaksOnMove reports a link that names its target through the old
// folder's path (absolute into it, or relative out of it), so it would point
// somewhere else after the move.
func linkBreaksOnMove(rel, target, source string) bool {
	if filepath.IsAbs(target) {
		_, inside := insideFolder(source, target)
		return inside
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

// listEntries renders entries with their sizes.
func listEntries(entries []MoveEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%s (%s)", e.Name, FormatBytes(e.Bytes)))
	}
	return listPaths(names)
}

// containsPath reports whether dir is path or one of its ancestors, by
// identity along both spellings of path (see insideFolder).
func containsPath(dir, path string) bool {
	if _, err := os.Stat(dir); err != nil {
		clean := filepath.Clean(path)
		return clean == filepath.Clean(dir) || within(clean, filepath.Clean(dir))
	}
	_, inside := insideFolder(dir, path)
	return inside
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
