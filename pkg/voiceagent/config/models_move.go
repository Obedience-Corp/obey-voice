package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Move phases, in the order a move reports them. Once MovePhaseSave is
// reported the move can no longer be cancelled.
const (
	MovePhaseMeasure = "measuring"
	MovePhaseCopy    = "copying"
	MovePhaseSave    = "saving"
	MovePhaseRemove  = "removing the old copy"
)

// MovePhaseCommitted reports whether a move in phase has saved the new folder
// and will finish even if cancelled.
func MovePhaseCommitted(phase string) bool {
	return phase == MovePhaseSave || phase == MovePhaseRemove
}

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
	// already safe in the new folder, such as an old folder it kept.
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
// It first runs the checks of PlanModelsMove and refuses with a
// *MoveRefusedError if any fails. Until the new folder is saved, a failure or
// cancellation leaves the old folder and the config as they were and removes
// only what the move itself created. After the save the move runs to the end:
// the old folder is set aside, anything written to it during the copy is
// brought over, and it is deleted only when every file in it is on disk in
// the new folder. Otherwise it is kept and a warning says where.
func MoveModels(ctx context.Context, cfg *Config, dest string, opts MoveOptions) (MoveResult, error) {
	report := opts.Progress
	if report == nil {
		report = func(MoveProgress) {}
	}
	report(MoveProgress{Phase: MovePhaseMeasure})
	plan, release, err := planModelsMove(ctx, cfg, dest)
	defer release()
	if err != nil {
		return MoveResult{}, err
	}
	if len(plan.Refusals) > 0 {
		return MoveResult{}, &MoveRefusedError{Reasons: plan.Refusals}
	}
	m := &mover{ctx: ctx, cfg: cfg, plan: plan, report: report,
		result: MoveResult{From: plan.From, To: plan.To, Bytes: plan.Bytes, Files: plan.Files}}
	if err := m.run(); err != nil {
		return MoveResult{}, err
	}
	return m.result, nil
}

type mover struct {
	ctx     context.Context
	cfg     *Config
	plan    MovePlan
	report  func(MoveProgress)
	result  MoveResult
	parents []string
	// keepOld is why the old folder must not be deleted, once saved.
	keepOld string
	// oldInPlace is set when the old folder was kept at its own path.
	oldInPlace bool
}

func (m *mover) warn(format string, args ...any) {
	m.result.Warnings = append(m.result.Warnings, fmt.Sprintf(format, args...))
}

func (m *mover) run() error {
	p := m.plan
	parents, err := makeParents(p.Dest)
	if err != nil {
		return err
	}
	m.parents = parents
	// os.Rename will not replace a folder, even an empty one. The resolved
	// destination is a real folder, never the user's link, and it is put
	// back if the rename fails.
	if p.destExisted {
		if err := os.Remove(p.Dest); err != nil {
			removeEmpty(m.parents)
			return fmt.Errorf("preparing %s: %w", p.Dest, err)
		}
	}
	err = moveHooks.rename(p.Source, p.Dest)
	if err == nil {
		return m.commitRename()
	}
	if p.destExisted {
		_ = os.Mkdir(p.Dest, p.destPerm)
	}
	if !isCrossDevice(err) {
		removeEmpty(m.parents)
		return fmt.Errorf("moving %s to %s: %w", p.Source, p.Dest, err)
	}
	return m.copyAndCommit()
}

func (m *mover) commitRename() error {
	p := m.plan
	undo := func(cause error) error {
		if err := moveHooks.rename(p.Dest, p.Source); err != nil {
			// Not wrapped: the move did not cleanly stop, whatever the cause.
			return fmt.Errorf("%v; the models could not be put back and are now in %s, while models_dir still names %s. Run `obey-voice config set models_dir %s` to use them", cause, p.Dest, p.From, p.To)
		}
		if p.destExisted {
			_ = os.Mkdir(p.Dest, p.destPerm)
		}
		removeEmpty(m.parents)
		return cause
	}
	if err := m.ctx.Err(); err != nil {
		return undo(err)
	}
	m.report(MoveProgress{Phase: MovePhaseSave})
	if err := m.save(); err != nil {
		return undo(err)
	}
	m.afterMove()
	return nil
}

func (m *mover) copyAndCommit() error {
	p := m.plan
	fail := func(d *destCopy, err error) error {
		if d != nil {
			d.discard()
		}
		removeEmpty(m.parents)
		return err
	}
	if reason := checkFreeSpace(existingAncestor(p.Dest), p.Bytes, new(int64)); reason != "" {
		return fail(nil, &MoveRefusedError{Reasons: []string{reason}})
	}
	m.result.Copied = true
	d, err := prepareDest(p.Dest, p.destExisted)
	if err != nil {
		return fail(nil, err)
	}
	m.report(MoveProgress{Phase: MovePhaseCopy, Total: p.Bytes})
	if err := copyModelsTree(m.ctx, p.Source, p.Dest, p.Bytes, m.report); err != nil {
		return fail(d, err)
	}
	if err := d.finish(); err != nil {
		return fail(d, fmt.Errorf("flushing the copy to disk: %w", err))
	}
	// Last point a cancel is honoured: once the setting is saved the move
	// runs to the end, so it never stops with the config naming a folder
	// that is about to be removed.
	if err := m.ctx.Err(); err != nil {
		return fail(d, err)
	}
	m.report(MoveProgress{Phase: MovePhaseSave})
	if err := m.save(); err != nil {
		return fail(d, err)
	}
	m.report(MoveProgress{Phase: MovePhaseRemove})
	m.retireOld()
	m.afterMove()
	return nil
}

// save writes models_dir and every repointed setting in one change, so they
// cannot disagree, and flushes it before anything old is deleted.
func (m *mover) save() error {
	pairs := append([]keyValue{{Key: "models_dir", Value: m.plan.To}}, m.plan.rewrites...)
	if err := setKeyFileValues(pairs, false); err != nil {
		return &moveSaveError{msg: describeSaveError(err), err: err}
	}
	traceMove("save")
	if err := syncFile(ConfigFile()); err != nil {
		m.keepOld = fmt.Sprintf("the new setting could not be flushed to disk (%v)", err)
	}
	m.result.values = map[string]string{}
	for _, kv := range m.plan.rewrites {
		m.result.values[kv.Key] = kv.Value.(string)
	}
	m.result.RewrittenKeys = m.plan.RewriteKeys
	m.result.ApplyTo(m.cfg)
	return nil
}

type moveSaveError struct {
	msg string
	err error
}

func (e *moveSaveError) Error() string { return e.msg }
func (e *moveSaveError) Unwrap() error { return e.err }

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// retireOld deletes the old folder of a copied move, but only once nothing in
// it is missing from the new one. It never deletes anything at the old path
// itself: the folder is renamed aside first, so a late writer gets an error
// instead of writing into a folder that is about to go.
func (m *mover) retireOld() {
	p := m.plan
	if m.keepOld != "" {
		m.oldInPlace = true
		m.warn("the old folder %s was kept because %s; delete it once you have checked the new one", p.Source, m.keepOld)
		return
	}
	old := quarantineName(p.Source)
	if err := moveHooks.rename(p.Source, old); err != nil {
		m.oldInPlace = true
		m.warn("the old folder %s was kept because it could not be set aside (%v); delete it once you have checked the new one", p.Source, err)
		return
	}
	// The move is committed; a cancel no longer stops it.
	differ, err := reconcile(context.WithoutCancel(m.ctx), old, p.Dest)
	switch {
	case err != nil:
		m.warn("the old folder was kept at %s because comparing it with the new one failed (%v); check it, then delete it", old, err)
		return
	case len(differ) > 0:
		m.warn("the old folder was kept at %s because %s kept changing during the move; check, then delete it", old, listPaths(differ))
		return
	}
	traceMove("remove %s", old)
	if err := os.RemoveAll(old); err != nil {
		m.warn("the old copy at %s could not be removed: %v", old, err)
	}
}

func (m *mover) afterMove() {
	p := m.plan
	if _, err := os.Lstat(p.Source); err == nil && !m.oldInPlace {
		m.warn("%s was created again while the move ran, so something is still writing models there; it was left in place", p.Source)
	}
	// models_dir was a link to the folder that moved; left in place it would
	// point at nothing.
	if info, err := os.Lstat(p.From); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if _, err := os.Stat(p.From); errors.Is(err, fs.ErrNotExist) {
			if err := os.Remove(p.From); err != nil {
				m.warn("the old link at %s could not be removed: %v", p.From, err)
			}
		}
	}
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
