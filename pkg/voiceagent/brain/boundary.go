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

// Boundary is the directory a worker must stay under: the working directory
// it was launched in, or that directory's campaign root when it sits inside
// one. No campaign is needed; the fence is the same either way.
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

// CheckPath refuses a file-tool path outside the boundary. It judges the
// exact string the tool will open (resolvePath), walked the way the kernel
// walks it, so the guard and the operation cannot disagree. File tools have
// no reason to leave the boundary: a worker that needs /etc has the shell.
func (b Boundary) CheckPath(workDir, path string) error {
	if !b.Enabled() || strings.TrimSpace(path) == "" {
		return nil
	}
	if b.inside(resolvePath(workDir, path)) {
		return nil
	}
	return b.refuse(path)
}

// CheckCommand refuses a shell command that names a place a worker has no
// business in: a path under the home directory but outside the boundary, a
// relative path that climbs or links out, or the campaign registry. The rest
// of the filesystem (/usr, /opt, /tmp) stays open. Other projects and every
// CLI's session store live under home, and that is what the incident was.
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
			return fmt.Errorf("camp list is not available to a worker; work under %s", b.Root)
		}
		expanded := b.expandHome(tok)
		switch {
		case filepath.IsAbs(expanded):
			if b.leavesHome(expanded) {
				return b.refuse(tok)
			}
		case climbs(expanded), b.firstSegmentExists(workDir, expanded):
			// A plain word is a path only if its first segment is one under
			// the working directory: a symlink there that points out must
			// not be a shorter way to say ~/.grok, for a file that exists or
			// one the command would create.
			if !b.inside(joinRaw(workDir, expanded)) {
				return b.refuse(tok)
			}
		}
	}
	return nil
}

// leavesHome reports an absolute path that lands under the home directory
// but outside the boundary, judged where it really points: a link under
// /tmp into ~/.grok is ~/.grok.
func (b Boundary) leavesHome(abs string) bool {
	if b.inside(abs) {
		return false
	}
	real, ok := resolve(abs)
	if !ok {
		return true
	}
	return b.underHome(abs) || b.underHome(real)
}

func (b Boundary) refuse(path string) error {
	return fmt.Errorf("%s is outside the work boundary %s", path, b.Root)
}

func (b Boundary) expandHome(tok string) string {
	if b.home == "" {
		return tok
	}
	switch {
	case tok == "~":
		return b.home
	case strings.HasPrefix(tok, "~/"):
		return b.home + tok[1:]
	case strings.HasPrefix(tok, "${HOME}"):
		return b.home + strings.TrimPrefix(tok, "${HOME}")
	case strings.HasPrefix(tok, "$HOME"):
		return b.home + strings.TrimPrefix(tok, "$HOME")
	}
	return tok
}

func (b Boundary) firstSegmentExists(workDir, rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	if first == "" || first == "." {
		return false
	}
	_, err := os.Lstat(filepath.Join(workDir, first))
	return err == nil
}

// joinRaw attaches a relative path to the working directory without cleaning
// it: "linked/.." must reach resolve with the link still in front of the "..".
func joinRaw(workDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return workDir + string(filepath.Separator) + path
}

// inside judges a path by where it really lands, so a symlink under the root
// that leads out is out (dangling or not, with or without a ".." behind it),
// and a root reached through a symlink still owns its real files.
func (b Boundary) inside(p string) bool {
	real, ok := resolve(p)
	if !ok {
		return false
	}
	root, ok := resolve(b.Root)
	if !ok {
		root = b.Root
	}
	return isUnder(real, root)
}

// maxLinks bounds symlink traversal in resolve, as the kernel's does.
const maxLinks = 40

// resolve walks an absolute path the way the kernel opens it: each symlink
// is followed where it stands (a relative target against the link's own
// directory), ".." applies to the directory reached so far rather than to
// the text, and components that do not exist yet are kept as written, which
// is where a new file would land. A dangling link is followed to its target.
// It reports false on a link cycle or an unreadable link.
func resolve(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	sep := string(filepath.Separator)
	cur := sep
	pending := splitPath(path)
	links := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil {
			return filepath.Join(append([]string{next}, pending...)...), true
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		links++
		if links > maxLinks {
			return "", false
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", false
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		pending = append(splitPath(target), pending...)
	}
	return cur, true
}

func splitPath(p string) []string {
	return strings.Split(p, string(filepath.Separator))
}

// underHome compares against home as given and as it really is, since a
// home directory can itself sit behind a symlink (/var on macOS).
func (b Boundary) underHome(abs string) bool {
	if b.home == "" {
		return false
	}
	if isUnder(abs, b.home) {
		return true
	}
	real, ok := resolve(b.home)
	return ok && isUnder(abs, real)
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
// The campaign line appears only when the root is one; a user without
// campaigns is never told about them.
func BoundaryContext(root string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nBoundary:\n- You are a background worker. Everything you read, write, or run stays under %s.\n", root)
	if config.CampaignFor(root).Found() {
		b.WriteString("- That directory is your campaign. Do not open other campaigns, and do not run camp list.\n")
	}
	b.WriteString("- Do not open other folders under the home directory or any tool's session store (~/.grok, ~/.claude, ~/.obey).\n")
	fmt.Fprintf(&b, "- Never run git commit or git push outside %s.\n", root)
	b.WriteString("- A task that needs anything past this boundary is refused: say what you could not do, and stop.")
	return b.String()
}

// workerDeniedTools is the CLI-side half of the fence for a worker: the
// campaign registry stays out of reach, whether or not this root is a
// campaign (the rule is inert where camp is not installed). Nil for the
// live brain and for a worker whose boundary is off, so nothing changes there.
func workerDeniedTools(cfg *config.Config) []string {
	if cfg == nil || strings.TrimSpace(cfg.WorkerBoundary) == "" {
		return nil
	}
	return []string{"Bash(camp list)", "Bash(camp list:*)", "Bash(camp list *)"}
}
