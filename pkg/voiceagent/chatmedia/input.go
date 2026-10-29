package chatmedia

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type inputKey struct{}

func WithInput(ctx context.Context, refs []Ref) context.Context {
	return context.WithValue(ctx, inputKey{}, append([]Ref(nil), refs...))
}
func Input(ctx context.Context) []Ref {
	refs, _ := ctx.Value(inputKey{}).([]Ref)
	return append([]Ref(nil), refs...)
}

// Paths recognizes local image paths without executing or expanding a shell.
func Paths(text string) ([]string, bool) {
	if paths, ok := parsePaths(text); ok {
		return paths, true
	}
	raw := strings.TrimSpace(text)
	if filepath.IsAbs(raw) && !strings.ContainsAny(raw, "\n\r\\\"'") && imageExtension(raw) {
		return []string{raw}, true
	}
	return nil, false
}
func imageExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}
func parsePaths(text string) ([]string, bool) {
	var paths []string
	var word strings.Builder
	var quote rune
	runes := []rune(strings.TrimSpace(text))
	flush := func() {
		if word.Len() > 0 {
			paths = append(paths, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (unicode.IsSpace(runes[i+1]) || runes[i+1] == '\\' || runes[i+1] == '"' || runes[i+1] == '\'') {
			i++
			word.WriteRune(runes[i])
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
		} else {
			word.WriteRune(r)
		}
	}
	flush()
	if quote != 0 || len(paths) == 0 || len(paths) > MaxAttachments {
		return nil, false
	}
	for i, p := range paths {
		if strings.HasPrefix(p, "file://") {
			u, err := url.Parse(p)
			if err != nil || u.Host != "" && u.Host != "localhost" {
				return nil, false
			}
			p = u.Path
		}
		if strings.HasPrefix(p, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, false
			}
			p = filepath.Join(home, p[2:])
		}
		if !imageExtension(p) {
			return nil, false
		}
		if !filepath.IsAbs(p) && !strings.HasPrefix(p, "./") {
			return nil, false
		}
		paths[i] = p
	}
	return paths, true
}
