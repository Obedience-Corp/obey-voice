package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// userHomeDir resolves the user home directory. Tests may override it via
// SetUserHomeDirForTest to avoid picking up real ~/.agents/skills.
var userHomeDir = os.UserHomeDir

// SetUserHomeDirForTest overrides home resolution for tests. The returned
// function restores the previous resolver; call it from t.Cleanup.
func SetUserHomeDirForTest(fn func() (string, error)) (restore func()) {
	prev := userHomeDir
	if fn == nil {
		userHomeDir = os.UserHomeDir
	} else {
		userHomeDir = fn
	}
	return func() { userHomeDir = prev }
}

// DefaultSearchPaths returns skill directories in precedence order (first match
// wins for duplicate skill names).
//
// Discovery is scoped to the conversation's working directory. Shared user
// skills are not imported implicitly, even when the working directory is home
// or ancestor discovery reaches it. A user can explicitly select any directory
// through configuredDir.
//
//  1. <workDir>/.agents/skills — project skills (Codex, VS Code, camp, …)
//  2. nearest ancestor .agents/skills — workspace/project-root skills
//  3. configuredDir — Obey Voice skills_dir (or its default under config)
//
// Ollama does not scan .claude/skills: that is Claude Code's native path and
// is handled by the Claude provider harness. Scanning both would duplicate
// skills when camp projects the same set into both locations.
//
// Empty or duplicate paths are omitted. Missing directories are fine — Catalog
// treats them as empty.
func DefaultSearchPaths(workDir, configuredDir string) []string {
	var paths []string
	seen := map[string]struct{}{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	home, _ := userHomeDir()
	if strings.TrimSpace(workDir) != "" {
		project := filepath.Join(workDir, ".agents", "skills")
		if home == "" || !sameDirectory(project, filepath.Join(home, ".agents", "skills")) {
			add(project)
		}
		if ancestor := ancestorAgentSkillsDir(workDir); ancestor != "" {
			add(ancestor)
		}
	}
	add(configuredDir)
	return paths
}

// ancestorAgentSkillsDir returns the nearest existing .agents/skills directory
// above workDir. The current directory is handled separately by
// DefaultSearchPaths so project-local skills keep precedence.
func ancestorAgentSkillsDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}

	resolved, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	home, _ := userHomeDir()

	for {
		if sameDirectory(resolved, home) {
			return ""
		}
		parent := filepath.Dir(resolved)
		if parent == resolved || sameDirectory(parent, home) {
			return ""
		}
		candidate := filepath.Join(parent, ".agents", "skills")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if home == "" || !sameDirectory(candidate, filepath.Join(home, ".agents", "skills")) {
				return candidate
			}
		}
		resolved = parent
	}
}

// sameDirectory recognizes aliases without requiring the directory to exist.
// This also prevents a project root symlink from re-importing shared skills.
func sameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	left, leftErr := filepath.Abs(a)
	right, rightErr := filepath.Abs(b)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
