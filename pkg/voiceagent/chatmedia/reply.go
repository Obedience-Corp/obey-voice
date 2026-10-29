package chatmedia

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Reply images are explicit standalone Markdown image references, not filenames
// found in arbitrary prose, tool output, or code. Never fetch remote URLs.
var replyImage = regexp.MustCompile(`^[\t ]*!\[([^\]\r\n]*)\]\((?:<([^>\r\n]+)>|([^()\r\n]+))\)[\t ]*$`)

// ImportReply runs on the owning response/job goroutine, before publication.
// Partial failures preserve the answer and return an explicit diagnostic.
func (s Store) ImportReply(text, workDir string) (string, []Ref, error) {
	lines := strings.Split(text, "\n")
	var refs []Ref
	var failures []error
	seen := map[string]bool{}
	fenced := false
	attempts := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		match := replyImage.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.TrimSpace(match[1])
		path := match[2]
		if path == "" {
			path = strings.TrimSpace(match[3])
		}
		var ref Ref
		var err error
		if attempts >= MaxAttachments {
			err = errors.New("at most four result images are allowed")
		} else {
			attempts++
			ref, err = s.importReplyFile(path, workDir)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("image result %q: %w", name, err))
			lines[i] = "[Image unavailable: " + name + "]"
			continue
		}
		if !seen[ref.ID] {
			refs = append(refs, ref)
			seen[ref.ID] = true
		}
		if name == "" {
			name = ref.Name
		}
		lines[i] = name
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), refs, errors.Join(failures...)
}

func (s Store) importReplyFile(path, workDir string) (Ref, error) {
	if strings.HasPrefix(path, "file://") {
		u, err := url.Parse(path)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return Ref{}, errors.New("use a local image file")
		}
		path = u.Path
	} else if strings.Contains(path, "://") {
		return Ref{}, errors.New("remote image URLs are not imported")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Ref{}, fmt.Errorf("resolve file: %w", err)
	}
	allowed := false
	for _, root := range []string{workDir, s.Dir} {
		if root == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			allowed = true
			break
		}
	}
	if !allowed {
		return Ref{}, errors.New("result image must be inside the agent work directory")
	}
	ref, err := s.ImportFile(resolved)
	if err != nil {
		return Ref{}, err
	}
	// Validate every animation frame before making the reference visible.
	if _, err = s.Preview(ref); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// ProtectImageExamples prevents later Markdown cleanup from turning fenced
// examples into standalone attachment directives. It does not read files.
func ProtectImageExamples(text string) string {
	lines := strings.Split(text, "\n")
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~") {
			fenced = !fenced
			continue
		}
		if fenced && replyImage.MatchString(line) {
			lines[i] = strings.Replace(line, "![", "[", 1)
		}
	}
	return strings.Join(lines, "\n")
}

// ReplyProse omits complete image directives when judging spoken sentence
// boundaries. An unfinished directive remains visible to truncation checks.
func ReplyProse(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !replyImage.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
