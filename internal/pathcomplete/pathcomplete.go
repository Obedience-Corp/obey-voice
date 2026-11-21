// Package pathcomplete completes @path mentions against a root directory and
// a campaign's navigation shortcuts (the same shortcuts `camp` uses).
//
// Every candidate is returned with its leading "@" and always contains a "/":
// directories end in "/", and a root-level file is written as "@./name". That
// keeps a completed path from ever reading as an @persona address.
package pathcomplete

import (
	"cmp"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Candidates returns completions for query, the text after "@".
//
// Without a "/", shortcuts whose key or path matches come first, then matching
// entries of root. With a "/", the text up to the last "/" names a directory
// under root and its entries are filtered by the rest. Prefix matches rank
// ahead of other matches. Hidden entries are skipped in listings.
func Candidates(query, root string, shortcuts map[string]string) []string {
	if strings.Contains(query, "/") {
		return listing(query, root)
	}
	out := shortcutMatches(query, shortcuts)
	seen := make(map[string]bool, len(out))
	for _, c := range out {
		seen[c] = true
	}
	for _, c := range entries(root, ".", query, true) {
		if !seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// ExpandShortcut reports the path a shortcut key stands for, so "@p/" can be
// rewritten to "@projects/" the moment the "/" is typed.
func ExpandShortcut(key string, shortcuts map[string]string) (string, bool) {
	if key == "" || strings.Contains(key, "/") {
		return "", false
	}
	path, ok := shortcuts[key]
	return path, ok && path != ""
}

// Rooted writes a candidate as an absolute path under root: "@projects/app/"
// becomes "@/root/projects/app/" and "@./README.md" becomes "@/root/README.md".
func Rooted(candidate, root string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(candidate, "@"), "./")
	return "@" + rootPrefix(root) + rel
}

// Unrooted turns a query written as an absolute path under root back into the
// relative query Candidates takes. Any other query is returned unchanged, so
// an absolute path outside root still lists nothing.
func Unrooted(query, root string) string {
	if root == "" {
		return query
	}
	rel, ok := strings.CutPrefix(query, rootPrefix(root))
	if !ok {
		return query
	}
	return rel
}

func rootPrefix(root string) string {
	return strings.TrimSuffix(filepath.ToSlash(root), "/") + "/"
}

// AtQuery finds an @ token at cursor in text. The "@" must start the text or
// follow whitespace, so "user@example.com" is not a token. Offsets are runes.
// It returns the text between "@" and cursor and the offset of "@", or -1.
func AtQuery(text string, cursor int) (query string, at int) {
	runes := []rune(text)
	cursor = min(max(cursor, 0), len(runes))
	start := cursor
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	if start == cursor || runes[start] != '@' {
		return "", -1
	}
	return string(runes[start+1 : cursor]), start
}

// FuzzyContains reports whether needle's runes appear in haystack in order,
// ignoring case.
func FuzzyContains(haystack, needle string) bool {
	h := []rune(haystack)
	i := 0
	for _, n := range needle {
		n = unicode.ToLower(n)
		for i < len(h) && unicode.ToLower(h[i]) != n {
			i++
		}
		if i == len(h) {
			return false
		}
		i++
	}
	return true
}

func hasPrefixFold(s, prefix string) bool {
	sr, pr := []rune(s), []rune(prefix)
	if len(pr) > len(sr) {
		return false
	}
	for i, r := range pr {
		if unicode.ToLower(sr[i]) != unicode.ToLower(r) {
			return false
		}
	}
	return true
}

type ranked struct {
	text, key string
	rank      int
}

func sortRanked(items []ranked) []string {
	slices.SortFunc(items, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), strings.Compare(strings.ToLower(a.key), strings.ToLower(b.key)), strings.Compare(a.text, b.text))
	})
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.text
	}
	return out
}

func shortcutMatches(query string, shortcuts map[string]string) []string {
	best := make(map[string]int)
	for key, path := range shortcuts {
		if path == "" {
			continue
		}
		rank := -1
		switch {
		case hasPrefixFold(key, query) || hasPrefixFold(path, query):
			rank = 0
		case FuzzyContains(key, query) || FuzzyContains(path, query):
			rank = 1
		}
		if rank < 0 {
			continue
		}
		display := "@" + path
		if prev, ok := best[display]; !ok || rank < prev {
			best[display] = rank
		}
	}
	items := make([]ranked, 0, len(best))
	for display, rank := range best {
		items = append(items, ranked{display, display, rank})
	}
	return sortRanked(items)
}

func listing(query, root string) []string {
	slash := strings.LastIndexByte(query, '/')
	return entries(root, query[:slash], query[slash+1:], false)
}

// entries lists dirRel under root, filtered by filter. A dirRel that is not
// local to root (absolute, empty, or climbing out with "..") lists nothing.
// bareDirs writes root-level directories as "@name/" rather than "@./name/".
func entries(root, dirRel, filter string, bareDirs bool) []string {
	if root == "" || !filepath.IsLocal(dirRel) {
		return nil
	}
	dir := filepath.Join(root, dirRel)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := "@" + dirRel + "/"
	items := make([]ranked, 0, len(list))
	for _, e := range list {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		rank := 0
		if !hasPrefixFold(name, filter) {
			if !FuzzyContains(name, filter) {
				continue
			}
			rank = 1
		}
		candidate := prefix + name
		if isDir(dir, e) {
			candidate += "/"
			if bareDirs {
				candidate = "@" + name + "/"
			}
		}
		items = append(items, ranked{candidate, name, rank})
	}
	return sortRanked(items)
}

func isDir(dir string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink == 0 {
		return e.IsDir()
	}
	info, err := os.Stat(filepath.Join(dir, e.Name()))
	return err == nil && info.IsDir()
}
