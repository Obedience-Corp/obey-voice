package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rootDir is "/": an existing directory, and never a workspace.
const rootDir = string(filepath.Separator)

// ErrNotADirectory reports a path that exists but is not a directory.
var ErrNotADirectory = errors.New("not a directory")

// executableDir reports the folder holding the running binary. A variable so a
// test can aim the guard below at a directory it controls, rather than at
// wherever `go test` happened to build its binary.
var executableDir = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// ResolveWorkDir resolves the agent's working directory for a caller that may
// carry an explicit override — a --work-dir flag.
//
// An override is taken literally: trimmed, ~-expanded, made absolute, and an
// ERROR when it is not an existing directory. Someone who types
// `--work-dir ~/Dev/projct` must be told, not silently handed their home folder
// with the agent's tools rooted in it.
//
// With no override the configured value decides, and there a bad path does fall
// back quietly (doctor warns about it) so a stale config.yaml never stops the
// agent from starting.
func ResolveWorkDir(cfg *Config, override string) (string, error) {
	if o := strings.TrimSpace(override); o != "" {
		dir, err := existingDir(o)
		if err != nil {
			return "", fmt.Errorf("work directory %q: %w", o, err)
		}
		return dir, nil
	}
	return WorkDirFrom(cfg), nil
}

// WorkDirFrom resolves the agent's working directory from configuration alone:
// cfg.WorkDir, then the configured work_dir, then the process cwd — except that
// the binary's own folder, "/", and anything that is not an existing directory
// fall back to the home directory.
//
// The binary-folder guard is the reason this exists: the Mac app spawns serve
// with cwd set to the install's bin directory, so every file tool, skills root
// and camp call landed there instead of somewhere the user chose.
//
// The result is always absolute and always an existing directory, so a caller
// can chdir into it and a client can display it. It is a default, not a
// sandbox — nothing here stops a tool from using an absolute path.
func WorkDirFrom(cfg *Config) string {
	candidate := ""
	if cfg != nil {
		candidate = strings.TrimSpace(cfg.WorkDir)
	}
	if candidate == "" {
		mu.RLock()
		candidate = strings.TrimSpace(v.GetString("work_dir"))
		mu.RUnlock()
	}
	if candidate == "" {
		if wd, err := os.Getwd(); err == nil {
			candidate = wd
		}
	}
	dir, err := existingDir(candidate)
	if err != nil || dir == rootDir || sameDir(dir, executableDir()) {
		return workDirFallback()
	}
	return dir
}

// WorkDir resolves the agent's working directory from global viper state, for
// callers (paths, doctor) that hold no Config.
func WorkDir() string {
	return WorkDirFrom(nil)
}

// existingDir trims, ~-expands and absolutizes path, and fails unless the
// result is an existing directory. The stat error already names the path, so
// callers add only their own context.
func existingDir(path string) (string, error) {
	path = expandHomePath(strings.TrimSpace(path))
	if path == "" {
		return "", os.ErrNotExist
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s: %w", abs, ErrNotADirectory)
	}
	return abs, nil
}

// workDirFallback is the last resort behind WorkDirFrom, and the reason the
// contract can promise an absolute existing directory. homeDir answers "." when
// the OS reports no home, and chdir'ing to "." would leave a spawned serve in
// the binary's folder — the exact failure the guards exist to prevent — so a
// home that is not usable falls through to the temp directory.
func workDirFallback() string {
	if home := homeDir(); filepath.IsAbs(home) {
		if dir, err := existingDir(home); err == nil && dir != rootDir && !sameDir(dir, executableDir()) {
			return dir
		}
	}
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return rootDir
	}
	return tmp
}

// sameDir compares directories through their symlinks, so an install reached
// via a symlinked bin (or macOS /var vs /private/var) still matches.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}
