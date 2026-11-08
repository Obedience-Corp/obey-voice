// Package archiveutil provides the filesystem boundary shared by managed tar installers.
package archiveutil

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Name validates the original slash-separated tar name before any prefix is
// removed. Reject portable absolute/volume forms even on Unix, and reject every
// parent component so normalization cannot hide an unsafe original name.
func Name(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe absolute or non-portable archive path %q", name)
	}
	for component := range strings.SplitSeq(name, "/") {
		if component == ".." {
			return "", fmt.Errorf("unsafe parent component in archive path %q", name)
		}
	}
	name = path.Clean(name)
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return name, nil
}

// Stripper removes one common release-directory prefix. Flat archives remain
// flat; entries outside an established prefix are rejected rather than merged.
type Stripper struct {
	prefix string
	set    bool
}

func (s *Stripper) Strip(original string, directory bool) (string, error) {
	name, err := Name(original)
	if err != nil {
		return "", err
	}
	if name == "." {
		return name, nil
	}
	if !s.set {
		s.set = true
		if first, _, ok := strings.Cut(name, "/"); ok {
			s.prefix = first + "/"
		} else if directory {
			s.prefix = name + "/"
		}
	}
	if s.prefix == "" {
		return name, nil
	}
	if name == strings.TrimSuffix(s.prefix, "/") {
		return ".", nil
	}
	if !strings.HasPrefix(name, s.prefix) {
		return "", fmt.Errorf("archive path %q is outside prefix %q", original, s.prefix)
	}
	return strings.TrimPrefix(name, s.prefix), nil
}

// Link is a delayed archive symlink. Name is relative to the extraction root;
// Target is interpreted relative to Name's parent using filesystem semantics.
type Link struct{ Name, Target string }

// CreateLinks runs only after all regular entries have been extracted into a
// private staging directory. It validates complete chains with Root.Stat, then
// flattens each alias to its resolved in-root target. No dangling links or cycles
// are installed. allowedTopLevels optionally limits targets to promoted entries.
func CreateLinks(root *os.Root, links []Link, allowedTopLevels ...string) error {
	for _, link := range links {
		if _, err := Name(link.Name); err != nil {
			return err
		}
		if link.Target == "" || strings.ContainsAny(link.Target, "\\:\x00") || path.IsAbs(link.Target) {
			return fmt.Errorf("unsafe archive symlink %q -> %q", link.Name, link.Target)
		}
		target := path.Join(path.Dir(link.Name), link.Target)
		if !filepath.IsLocal(filepath.FromSlash(target)) {
			return fmt.Errorf("archive symlink %q escapes root", link.Name)
		}
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(link.Name)), 0755); err != nil {
			return err
		}
		// Do not replace an existing file, directory, or earlier link. In
		// particular, a file beneath a pending link makes this fail closed.
		if err := root.Symlink(link.Target, filepath.FromSlash(link.Name)); err != nil {
			return fmt.Errorf("create archive symlink %q: %w", link.Name, err)
		}
	}
	if len(links) == 0 {
		return nil
	}
	canonicalRoot, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		return err
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return err
	}
	targets := make([]string, len(links))
	for i, link := range links {
		// Root.Stat resolves the actual chain, including '..' after symlink
		// components, and refuses traversal outside root. Do not clean the
		// link target and treat that lexical result as its resolved location.
		if _, err := root.Stat(filepath.FromSlash(link.Name)); err != nil {
			return fmt.Errorf("resolve archive symlink %q: %w", link.Name, err)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(canonicalRoot, filepath.FromSlash(link.Name)))
		if err != nil {
			return fmt.Errorf("resolve archive symlink %q: %w", link.Name, err)
		}
		rel, err := filepath.Rel(canonicalRoot, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("archive symlink %q escapes root", link.Name)
		}
		if len(allowedTopLevels) > 0 {
			top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			allowed := false
			for _, name := range allowedTopLevels {
				if top == name {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("archive symlink %q targets an entry that is not promoted: %q", link.Name, rel)
			}
		}
		targets[i], err = filepath.Rel(filepath.FromSlash(path.Dir(link.Name)), rel)
		if err != nil {
			return err
		}
	}
	// All chains are proven safe before rewriting any of them. Canonical
	// relative targets retain their meaning after rename or cross-device copy,
	// even when an intermediate link's top-level directory is not promoted.
	for i, link := range links {
		if err := root.Remove(filepath.FromSlash(link.Name)); err != nil {
			return err
		}
		if err := root.Symlink(targets[i], filepath.FromSlash(link.Name)); err != nil {
			return err
		}
	}
	return nil
}
