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

// Reply images are explicit Markdown image references that end their line,
// outside code, not filenames found in arbitrary prose or tool output. A
// reference followed by other text is a mention. Never fetch remote URLs.
const imageReference = `!\[([^\]\r\n]*)\]\((?:<([^>\r\n]+)>|([^()\r\n]+))\)`

var (
	inlineImage = regexp.MustCompile(imageReference)
	replyImage  = regexp.MustCompile(`^[\t ]*` + imageReference + `[\t ]*\r?$`)
	bareMarker  = regexp.MustCompile(`^[\t ]*(?:>[\t ]*)*(?:[-*+•]|(?:[0-9]+|[A-Za-z]|[ivxIVX]{2,4})[.)])?[\t ]*$`)
	bulletOnly  = regexp.MustCompile(`^[\t ]*(?:>[\t ]*)*(?:[-*+•][\t ]*)?$`)
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
		if IsFence(line) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
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
			lines[i] = "[Image unavailable: " + name + "]" + cr
			continue
		}
		if !seen[ref.ID] {
			refs = append(refs, ref)
			seen[ref.ID] = true
		}
		if name == "" {
			name = ref.Name
		}
		lines[i] = name + cr
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
		fence := IsFence(line)
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

// SeparateImageReferences moves the references that end a line of prose onto
// their own lines after it. Code, mentions followed by other text, and every
// other line are left as written.
func SeparateImageReferences(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	fenced := false
	for _, line := range lines {
		if IsFence(line) {
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
	prose, refs := TrailingImageReferences(line)
	if len(refs) == 0 {
		return []string{line}
	}
	cr := ""
	if strings.HasSuffix(line, "\r") {
		cr = "\r"
	}
	out := make([]string, 0, len(refs)+1)
	if !bulletOnly.MatchString(prose) {
		out = append(out, prose+cr)
	}
	for _, ref := range refs {
		out = append(out, ref+cr)
	}
	return out
}

// TrailingImageReferences splits a line into its prose and the run of
// references that ends it, with only spaces between them. References in code
// spans or escaped with a backslash do not count. With no such run it returns
// the line unchanged.
func TrailingImageReferences(line string) (string, []string) {
	body := strings.TrimSuffix(line, "\r")
	spans := codeSpans(body)
	ms := inlineImage.FindAllStringIndex(body, -1)
	end := len(strings.TrimRight(body, " \t"))
	first := len(ms)
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m[1] != end || inCodeSpan(spans, m, 1) || escaped(body, m[0]) {
			break
		}
		first, end = i, len(strings.TrimRight(body[:m[0]], " \t"))
	}
	if first == len(ms) {
		return line, nil
	}
	refs := make([]string, 0, len(ms)-first)
	for _, m := range ms[first:] {
		refs = append(refs, body[m[0]:m[1]])
	}
	return body[:end], refs
}

// BareMarker reports whether what is left of a line is blank or only a list
// or quote marker, ordinals included, as after removing "![A](a.png)" from
// "1. ![A](a.png)".
func BareMarker(line string) bool {
	return bareMarker.MatchString(line)
}

// IsFence reports whether a line opens or closes a fenced block: it starts
// with ``` and does not close it on the same line, which would be a span, or
// it starts with ~~~, which has no inline form.
func IsFence(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "~~~") {
		return true
	}
	return strings.HasPrefix(trimmed, "```") && strings.Count(trimmed, "```")%2 == 1
}

// escaped reports whether the byte at i is preceded by an odd number of
// backslashes, as in the literal Markdown "\![caption](path)".
func escaped(line string, i int) bool {
	n := 0
	for i > 0 && line[i-1] == '\\' {
		n++
		i--
	}
	return n%2 == 1
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
		if s[2] >= minTicks && s[0] <= m[0] && m[1] <= s[1] {
			return true
		}
	}
	return false
}
