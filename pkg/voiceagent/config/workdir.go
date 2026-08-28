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

// WorkDirSource names the layer that decided the working directory.
//
// It is the difference between a launch the user can explain and one they
// cannot. flag, config and launch-dir are all things someone typed; env and the
// two fallbacks move the agent somewhere nobody chose, and until they were
// reported a hijacked launch looked identical to a normal one (WI-742a80).
type WorkDirSource string

const (
	WorkDirSourceFlag         WorkDirSource = "flag"
	WorkDirSourceConfig       WorkDirSource = "config"
	WorkDirSourceEnv          WorkDirSource = "env"
	WorkDirSourceLaunchDir    WorkDirSource = "launch-dir"
	WorkDirSourceHomeFallback WorkDirSource = "home-fallback"
	WorkDirSourceTempFallback WorkDirSource = "temp-fallback"
)

// WorkDirInfo is a resolved working directory plus the provenance of the value.
// Dir keeps the contract the resolver has always promised: absolute, existing,
// chdir-able — empty only alongside a resolution error.
type WorkDirInfo struct {
	Dir    string
	Source WorkDirSource
	// EnvVar names the environment variable in play: the one that decided Dir
	// when Source is WorkDirSourceEnv, or — when a guard rejected that value and
	// Source is a fallback — the one whose value was rejected. work_dir is bound
	// to more than one name, so the variable that actually won is named rather
	// than implied.
	EnvVar string
	// Rejected is the candidate a guard refused, and RejectedSource the layer
	// that offered it. Set only on a fallback, and the reason one is legible: a
	// typo'd WORK_DIR otherwise lands the agent in the home directory with
	// nothing naming the variable that sent it there.
	Rejected       string
	RejectedSource WorkDirSource
}

// Describe renders the provenance as one human phrase, for the serve banner,
// `paths`, and the doctor row. It names the deciding environment variable
// rather than the binding in the abstract: "an env var" sends the user hunting,
// and the whole defect was not knowing which name to look for.
func (i WorkDirInfo) Describe() string {
	switch i.Source {
	case WorkDirSourceHomeFallback, WorkDirSourceTempFallback:
		return workDirSourcePhrase(i.Source, "") + i.rejectedSuffix()
	default:
		return workDirSourcePhrase(i.Source, i.EnvVar)
	}
}

// rejectedSuffix names what a fallback replaced. Without it the two fallbacks
// report only that nothing usable was found, and the user is left hunting for
// the variable or setting that pointed at the unusable path.
func (i WorkDirInfo) rejectedSuffix() string {
	if i.Rejected == "" {
		return " (nothing named a usable directory)"
	}
	return fmt.Sprintf(" (%s pointed at %s, which is not a usable directory)",
		workDirSourcePhrase(i.RejectedSource, i.EnvVar), i.Rejected)
}

// workDirSourcePhrase is the one place a source becomes words, so the phrase a
// fallback uses for the value it rejected matches the phrase that value would
// have printed had it been accepted.
func workDirSourcePhrase(source WorkDirSource, envVar string) string {
	switch source {
	case WorkDirSourceFlag:
		return "the --work-dir flag"
	case WorkDirSourceConfig:
		return "the work_dir setting"
	case WorkDirSourceEnv:
		if envVar == "" {
			return "an environment variable"
		}
		return "the " + envVar + " environment variable"
	case WorkDirSourceLaunchDir:
		return "the directory Obey Voice was launched from"
	case WorkDirSourceHomeFallback:
		return "the home-directory fallback"
	case WorkDirSourceTempFallback:
		return "the temp-directory fallback"
	default:
		return "an unknown source"
	}
}

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
	info, err := ResolveWorkDirInfo(cfg, override)
	return info.Dir, err
}

// ResolveWorkDirInfo is ResolveWorkDir with the provenance of the answer, for
// the surfaces that report where the working directory came from.
func ResolveWorkDirInfo(cfg *Config, override string) (WorkDirInfo, error) {
	if o := strings.TrimSpace(override); o != "" {
		dir, err := existingDir(o)
		if err != nil {
			return WorkDirInfo{}, fmt.Errorf("work directory %q: %w", o, err)
		}
		return WorkDirInfo{Dir: dir, Source: WorkDirSourceFlag}, nil
	}
	return WorkDirInfoFrom(cfg), nil
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
	return WorkDirInfoFrom(cfg).Dir
}

// WorkDirInfoFrom is WorkDirFrom with the provenance of the answer. The
// resolution order and every guard are WorkDirFrom's — this is the one
// implementation, and the plain form is the projection of it.
func WorkDirInfoFrom(cfg *Config) WorkDirInfo {
	candidate, source, envVar := workDirCandidate(cfg)
	dir, err := existingDir(candidate)
	if err != nil || dir == rootDir || sameDir(dir, executableDir()) {
		info := workDirFallback()
		// The fallback is the honest source — that directory is where the agent
		// works — but the rejected candidate is what the user has to fix, so it
		// travels with it instead of being dropped.
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			info.Rejected, info.RejectedSource, info.EnvVar = candidate, source, envVar
		}
		return info
	}
	return WorkDirInfo{Dir: dir, Source: source, EnvVar: envVar}
}

// workDirCandidate returns the first value the resolution order offers, and the
// layer it came from: cfg.WorkDir, then the configured work_dir, then the
// process cwd. The value is returned unvalidated — the guards belong to the
// caller, so a rejected candidate becomes a fallback rather than a silent
// promotion of the next layer.
func workDirCandidate(cfg *Config) (candidate string, source WorkDirSource, envVar string) {
	if cfg != nil {
		if c := strings.TrimSpace(cfg.WorkDir); c != "" {
			src, name := workDirValueSource(c)
			return c, src, name
		}
	}
	mu.RLock()
	configured := strings.TrimSpace(v.GetString("work_dir"))
	mu.RUnlock()
	if configured != "" {
		src, name := workDirValueSource(configured)
		return configured, src, name
	}
	if wd, err := os.Getwd(); err == nil {
		return wd, WorkDirSourceLaunchDir, ""
	}
	// No cwd to launch from: the caller's guards turn this into a fallback,
	// which carries its own source.
	return "", WorkDirSourceLaunchDir, ""
}

// workDirValueSource attributes a non-empty work_dir value to the layer that
// produced it.
//
// Viper layers the environment over the file and reports one merged value, so
// v.GetString alone cannot tell them apart. The bound variables are therefore
// looked up directly, and only a variable whose value IS the candidate can be
// the one that decided it — an env var that lost to an explicit cfg.WorkDir
// must not be blamed for the answer.
func workDirValueSource(candidate string) (WorkDirSource, string) {
	if name, value, ok := envValueFor("work_dir"); ok && strings.TrimSpace(value) == candidate {
		return WorkDirSourceEnv, name
	}
	return WorkDirSourceConfig, ""
}

// WorkDir resolves the agent's working directory from global viper state, for
// callers (paths, doctor) that hold no Config.
func WorkDir() string {
	return CurrentWorkDirInfo().Dir
}

// CurrentWorkDirInfo is WorkDir with provenance, the twin those same
// Config-less callers need to report where the directory came from.
func CurrentWorkDirInfo() WorkDirInfo {
	return WorkDirInfoFrom(nil)
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
func workDirFallback() WorkDirInfo {
	if home := homeDir(); filepath.IsAbs(home) {
		if dir, err := existingDir(home); err == nil && dir != rootDir && !sameDir(dir, executableDir()) {
			return WorkDirInfo{Dir: dir, Source: WorkDirSourceHomeFallback}
		}
	}
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return WorkDirInfo{Dir: rootDir, Source: WorkDirSourceTempFallback}
	}
	return WorkDirInfo{Dir: tmp, Source: WorkDirSourceTempFallback}
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
