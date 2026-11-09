package textclean

import (
	"errors"
	"strings"
)

// ErrRawToolMarkup means the model put an invocation in its text channel.
// Typed tool calls are the only executable channel; this text is never parsed
// into a call. Callers must report this error through their normal error path.
var ErrRawToolMarkup = errors.New("model returned raw tool-call markup instead of a typed tool call; the raw invocation was not executed")

// ToolMarkupFilter prevents invocation syntax from escaping a streaming reply.
// Ordinary angle brackets and Markdown code quotations pass unchanged. Once
// invocation syntax is confirmed, the generation is invalid: the remaining text
// is withheld and Err explains why. Keep one filter per model generation.
type ToolMarkupFilter struct {
	pending   string
	err       error
	codeTicks int
	tickRun   int
	escaped   bool
}

func (f *ToolMarkupFilter) Err() error { return f.err }

func (f *ToolMarkupFilter) Feed(chunk string) string {
	if f.err != nil {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		ch := chunk[i]
		if f.pending != "" {
			f.pending += string(ch)
			possible, confirmed := toolMarkerPrefix(f.pending)
			if confirmed {
				f.err = ErrRawToolMarkup
				f.pending = ""
				break
			}
			if possible {
				continue
			}
			// A second '<' may start a marker even when the first was prose.
			if ch == '<' {
				out.WriteString(f.pending[:len(f.pending)-1])
				f.pending = "<"
			} else {
				out.WriteString(f.pending[:len(f.pending)-1])
				f.pending = ""
				i--
			}
			continue
		}
		if ch == '`' && !f.escaped {
			f.tickRun++
			out.WriteByte(ch)
			continue
		}
		if f.tickRun > 0 {
			if f.codeTicks == 0 {
				f.codeTicks = f.tickRun
			} else if f.codeTicks == f.tickRun {
				f.codeTicks = 0
			}
			f.tickRun = 0
		}
		if ch == '<' && f.codeTicks == 0 && !f.escaped {
			f.pending = "<"
		} else {
			out.WriteByte(ch)
		}
		if ch == '\\' {
			f.escaped = !f.escaped
		} else {
			f.escaped = false
		}
	}
	return out.String()
}

// Flush releases a genuinely ambiguous prefix. A recognizable truncated tool
// marker is rejected at EOF, rather than being pronounced as literal markup.
func (f *ToolMarkupFilter) Flush() string {
	if f.err != nil {
		return ""
	}
	pending := f.pending
	f.pending = ""
	lower := strings.ToLower(strings.TrimSpace(pending))
	for _, marker := range []string{"<tool_call", "<tool_calls", "</tool_call", "</tool_calls", "</function", "</parameter", "<atem:function_calls", "</atem:function_calls", "<atem:invoke", "</atem:invoke", "<atem:parameter", "</atem:parameter"} {
		if lower == marker {
			f.err = ErrRawToolMarkup
			return ""
		}
	}
	return pending
}

// toolMarkerPrefix distinguishes tool-specific syntax from comparisons or
// ordinary tags. It holds at most the fixed marker;
// once '=' or a tag delimiter confirms the grammar, no payload is buffered.
func toolMarkerPrefix(s string) (possible, confirmed bool) {
	lower := strings.ToLower(s)
	for _, base := range []string{"<tool_call", "<tool_calls", "</tool_call", "</tool_calls", "</function", "</parameter", "<function", "<parameter", "<atem:function_calls", "</atem:function_calls", "<atem:invoke", "</atem:invoke", "<atem:parameter", "</atem:parameter"} {
		if strings.HasPrefix(base, lower) {
			possible = true
			continue
		}
		if !strings.HasPrefix(lower, base) {
			continue
		}
		rest := lower[len(base):]
		if base == "<function" || base == "<parameter" {
			if strings.HasPrefix(rest, "=") {
				return true, true
			}
			continue
		}
		if len(rest) > 0 && strings.ContainsRune(" \t\r\n>/", rune(rest[0])) {
			return true, true
		}
	}
	return possible, false
}

// StripToolMarkup applies the same streaming grammar to a complete reply.
func StripToolMarkup(s string) (string, error) {
	var f ToolMarkupFilter
	result := f.Feed(s) + f.Flush()
	return result, f.Err()
}
