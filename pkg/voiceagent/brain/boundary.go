package brain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Boundary is the directory a worker must stay under: the campaign root it
// was launched in, or the working directory when there is no campaign.
//
// A worker starts in the right place and nothing kept it there. On 2026-09-06
// one listed the CLI's session store under home, read another thread, and
// committed to a repository in a different campaign. The boundary is a fence
// on the tools Obey Voice runs itself (the Ollama worker) and a rule in the
// prompt for the tools a CLI runs on its own.
type Boundary struct {
	Root string
	home string
}

// NewBoundary fences root. An empty root is no boundary at all.
func NewBoundary(root string) Boundary {
	root = strings.TrimSpace(root)
	if root == "" {
		return Boundary{}
	}
	home, _ := os.UserHomeDir()
	return Boundary{Root: filepath.Clean(root), home: filepath.Clean(home)}
}

// Enabled reports whether there is a fence.
func (b Boundary) Enabled() bool { return b.Root != "" }

// check applies the boundary to one tool call before it runs.
func (b Boundary) check(workDir string, call api.ToolCall) error {
	if !b.Enabled() {
		return nil
	}
	args := call.Function.Arguments.ToMap()
	switch call.Function.Name {
	case "list_files":
		path, _ := args["path"].(string)
		if path == "" {
			path = "."
		}
		return b.CheckPath(workDir, path)
	case "read_file", "write_file":
		path, _ := args["path"].(string)
		return b.CheckPath(workDir, path)
	case "run_command":
		command, _ := args["command"].(string)
		return b.CheckCommand(workDir, command)
	}
	return nil
}

// CheckPath refuses a file-tool path outside the boundary. File tools have
// no reason to leave it: a worker that needs /etc has the shell.
func (b Boundary) CheckPath(workDir, path string) error {
	if !b.Enabled() || strings.TrimSpace(path) == "" {
		return nil
	}
	if b.inside(b.absolute(workDir, path)) {
		return nil
	}
	return b.refuse(path)
}

// CheckCommand refuses a shell command that names a place a worker has no
// business in: a path under the home directory but outside the boundary, a
// relative path that climbs out, or the campaign registry. The rest of the
// filesystem (/usr, /opt, /tmp) stays open. Other campaigns and every CLI's
// session store live under home, and that is what the incident was.
//
// This reads the command's tokens; it is a fence, not a sandbox. A command
// that hides a path from it still runs with the working directory as cwd.
func (b Boundary) CheckCommand(workDir, command string) error {
	if !b.Enabled() {
		return nil
	}
	toks := commandTokens(command)
	for i, tok := range toks {
		if filepath.Base(tok) == "camp" && i+1 < len(toks) && toks[i+1] == "list" {
			return fmt.Errorf("camp list is not available to a worker; this campaign is %s", b.Root)
		}
		expanded := b.expandHome(tok)
		switch {
		case filepath.IsAbs(expanded):
			if b.underHome(expanded) && !b.inside(expanded) {
				return b.refuse(tok)
			}
		case climbs(expanded):
			if !b.inside(b.absolute(workDir, expanded)) {
				return b.refuse(tok)
			}
		}
	}
	return nil
}

func (b Boundary) refuse(path string) error {
	return fmt.Errorf("%s is outside the campaign boundary %s", path, b.Root)
}

func (b Boundary) expandHome(tok string) string {
	if b.home == "" {
		return tok
	}
	switch {
	case tok == "~":
		return b.home
	case strings.HasPrefix(tok, "~/"):
		return filepath.Join(b.home, tok[2:])
	case strings.HasPrefix(tok, "${HOME}"):
		return b.home + strings.TrimPrefix(tok, "${HOME}")
	case strings.HasPrefix(tok, "$HOME"):
		return b.home + strings.TrimPrefix(tok, "$HOME")
	}
	return tok
}

func (b Boundary) absolute(workDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

// inside is lexical first, then through symlinks for paths that exist, so a
// root reached by a symlink does not refuse its own real files.
func (b Boundary) inside(abs string) bool {
	if isUnder(abs, b.Root) {
		return true
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(b.Root)
	if err != nil {
		return false
	}
	return isUnder(real, realRoot)
}

func (b Boundary) underHome(abs string) bool {
	return b.home != "" && isUnder(abs, b.home)
}

func isUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

func climbs(tok string) bool {
	return tok == ".." || strings.HasPrefix(tok, "../") || strings.Contains(tok, "/../") || strings.HasSuffix(tok, "/..")
}

// commandTokens splits a shell command on whitespace and the operators that
// separate words, and strips quotes, so `cd "/x" && git -C=/y` yields /x and /y.
func commandTokens(command string) []string {
	split := func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";|&()<>=`", r)
	}
	var toks []string
	for _, t := range strings.FieldsFunc(command, split) {
		if t = strings.Trim(t, `"'`); t != "" {
			toks = append(toks, t)
		}
	}
	return toks
}

// BoundaryContext is the prompt element every worker gets, whichever brain
// runs it. The Ollama worker also has the fence; a CLI worker has only this.
func BoundaryContext(root string) string {
	return fmt.Sprintf(`
Boundary:
- You are a background worker for this campaign only. Everything you read, write, or run stays under %s.
- Do not open other campaigns, other folders under the home directory, or any tool's session store (~/.grok, ~/.claude, ~/.obey). Do not run camp list; this is your campaign.
- Never run git commit or git push outside %s.
- A task that needs anything past this boundary is refused: say what you could not do, and stop.`, root, root)
}

// workerDeniedTools is the CLI-side half of the fence for a worker: the
// campaign registry stays out of reach. Nil for the live brain and for a
// worker whose boundary is off, so nothing changes there.
func workerDeniedTools(cfg *config.Config) []string {
	if cfg == nil || strings.TrimSpace(cfg.WorkerBoundary) == "" {
		return nil
	}
	return []string{"Bash(camp list)", "Bash(camp list:*)", "Bash(camp list *)"}
}
