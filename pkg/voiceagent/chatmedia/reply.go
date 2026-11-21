package chatmedia

import (
	"bytes"
	"errors"
	"fmt"
	"image/gif"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Reply images are explicit Markdown image references outside code, not
// filenames found in arbitrary prose or tool output. Never fetch remote URLs.
const imageReference = `!\[([^\]\r\n]*)\]\((?:<([^>\r\n]+)>|([^()\r\n]+))\)`

var (
	inlineImage = regexp.MustCompile(imageReference)
	replyImage  = regexp.MustCompile(`^[\t ]*` + imageReference + `[\t ]*$`)
	bareMarker  = regexp.MustCompile(`^[\t ]*(?:>[\t ]*)*(?:[-*+•]|[0-9]+[.)])?[\t ]*$`)
)

// ImportReply runs on the owning response/job goroutine, before publication.
// Partial failures preserve the answer and return an explicit diagnostic.
func (s Store) ImportReply(text, workDir string) (string, []Ref, error) {
	return s.processReply(text, workDir, s.importReplyFile)
}

// CheckReplyFiles reports an error when every explicit result is unusable.
// The normal reply importer owns persistence and reports partial failures.
func (s Store) CheckReplyFiles(text, workDir string) error {
	_, refs, err := s.processReply(text, workDir, inspectReplyFile)
	if len(refs) > 0 {
		return nil
	}
	return err
}

func inspectReplyFile(path string) (Ref, error) {
	data, err := readImageFile(path)
	if err != nil {
		return Ref{}, err
	}
	ref, _, err := decodeImage(filepath.Base(path), data, true)
	if err == nil && ref.Format == "gif" {
		// Inspect checks the animation bounds and decodes the first frame.
		// A later frame must also be decodable for the preview to succeed.
		_, err = gif.DecodeAll(bytes.NewReader(data))
	}
	return ref, err
}

func (s Store) processReply(text, workDir string, load func(string) (Ref, error)) (string, []Ref, error) {
	lines := strings.Split(SeparateImageReferences(text), "\n")
	var refs []Ref
	var failures []error
	seen := map[string]bool{}
	fenced := false
	attempts := 0
	for i, line := range lines {
		if isFence(line) {
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
			var resolved string
			resolved, err = s.resolveReplyFile(path, workDir)
			if err == nil {
				ref, err = load(resolved)
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("image result %q (%q): %w", name, path, err))
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

func (s Store) resolveReplyFile(path, workDir string) (string, error) {
	if strings.HasPrefix(path, "file://") {
		u, err := url.Parse(path)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return "", errors.New("use a local image file")
		}
		path = u.Path
	} else if strings.Contains(path, "://") {
		return "", errors.New("remote image URLs are not imported")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve file: %w", err)
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
		return "", errors.New("result image must be inside the agent work directory")
	}
	return resolved, nil
}

func (s Store) importReplyFile(resolved string) (Ref, error) {
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
// examples into attachment directives. Cleanup also unwraps ``` code spans.
// It does not read files.
func ProtectImageExamples(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	lines := strings.Split(text, "\n")
	fenced := false
	for i, line := range lines {
		fence := isFence(line)
		if fence || fenced {
			lines[i] = inlineImage.ReplaceAllStringFunc(line, inertImage)
			if fence {
				fenced = !fenced
			}
			continue
		}
		var b strings.Builder
		last := 0
		spans := codeSpans(line)
		for _, m := range inlineImage.FindAllStringIndex(line, -1) {
			if inCodeSpan(spans, m, 3) {
				b.WriteString(line[last:m[0]])
				b.WriteString(inertImage(line[m[0]:m[1]]))
				last = m[1]
			}
		}
		if last > 0 {
			b.WriteString(line[last:])
			lines[i] = b.String()
		}
	}
	return strings.Join(lines, "\n")
}

func inertImage(ref string) string { return ref[1:] }

// RemoveImageReferences deletes every complete image reference, code included.
func RemoveImageReferences(text string) string {
	return inlineImage.ReplaceAllString(text, "")
}

// ReplyProse omits complete image directives when judging spoken sentence
// boundaries. An unfinished directive remains visible to truncation checks.
func ReplyProse(text string) string {
	lines := strings.Split(SeparateImageReferences(text), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !replyImage.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// SeparateImageReferences moves each image reference written inside a line of
// prose onto its own line after that prose, so a reference counts the same
// wherever the model wrote it. Code and every other line are left as written.
func SeparateImageReferences(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	fenced := false
	for _, line := range lines {
		if isFence(line) {
			fenced = !fenced
		} else if !fenced && !replyImage.MatchString(line) {
			out = append(out, separateLine(line)...)
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func separateLine(line string) []string {
	var refs []string
	for {
		var at [][]int
		spans := codeSpans(line)
		for _, m := range inlineImage.FindAllStringIndex(line, -1) {
			if !inCodeSpan(spans, m, 1) {
				at = append(at, m)
			}
		}
		if len(at) == 0 {
			break
		}
		prose := line[:at[0][0]]
		for i, m := range at {
			refs = append(refs, line[m[0]:m[1]])
			end := len(line)
			if i+1 < len(at) {
				end = at[i+1][0]
			}
			prose = joinProse(prose, line[m[1]:end])
		}
		line = prose
	}
	if len(refs) == 0 {
		return []string{line}
	}
	if BareMarker(line) {
		return refs
	}
	return append([]string{strings.TrimRight(line, " \t\r")}, refs...)
}

// BareMarker reports whether what is left of a line is blank or only a list
// or quote marker, as after removing the pictures from "- ![A](a.png)".
func BareMarker(line string) bool {
	return bareMarker.MatchString(line)
}

// joinProse closes the gap a removed reference leaves without doubling spaces.
func joinProse(left, right string) string {
	switch {
	case strings.TrimSpace(left) == "":
		return left + strings.TrimLeft(right, " \t")
	case strings.TrimSpace(right) == "":
		return strings.TrimRight(left, " \t")
	case strings.ContainsRune(" \t,.;:!?", rune(right[0])):
		return strings.TrimRight(left, " \t") + right
	}
	return left + right
}

func isFence(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

// codeSpans returns backtick code spans on one line as start, end and the
// length of the backtick run that delimits them.
func codeSpans(line string) [][3]int {
	var spans [][3]int
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		open := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		n := i - open
		for j := i; j < len(line); {
			if line[j] != '`' {
				j++
				continue
			}
			run := j
			for j < len(line) && line[j] == '`' {
				j++
			}
			if j-run == n {
				spans = append(spans, [3]int{open, j, n})
				i = j
				break
			}
		}
	}
	return spans
}

func inCodeSpan(spans [][3]int, m []int, minTicks int) bool {
	for _, s := range spans {
		if s[2] >= minTicks && m[0] < s[1] && s[0] < m[1] {
			return true
		}
	}
	return false
}
