package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func formatWorkerDetail(item pipeline.WorkerInfo, width int) string {
	width = max(1, width)
	var b strings.Builder
	b.WriteString(normalStyle.Render(compactWorkerText(item.Task)))
	b.WriteString("\n\n")
	b.WriteString(workerStateStyle(item.State).Render(item.State))
	b.WriteString("  ")
	b.WriteString(metaStyle.Render(workerProvider(item)))
	b.WriteString(dimStyle.Render("  " + roundSeconds(item.Elapsed)))
	b.WriteByte('\n')
	b.WriteString(dimStyle.Render("ID  " + item.ID))
	b.WriteByte('\n')
	if item.Activity != "" {
		b.WriteByte('\n')
		b.WriteString(sectionStyle.Render("Activity"))
		b.WriteByte('\n')
		b.WriteString(formatWorkerPayload(item.Activity, width))
		b.WriteByte('\n')
	}
	if item.Result != "" {
		b.WriteByte('\n')
		b.WriteString(sectionStyle.Render("Output"))
		b.WriteByte('\n')
		b.WriteString(formatWorkerPayload(item.Result, width))
		b.WriteByte('\n')
	}
	if item.Err != "" {
		b.WriteByte('\n')
		b.WriteString(sectionStyle.Render("Error"))
		b.WriteByte('\n')
		b.WriteString(errorStyle.Render(wrapWorkerPlain(item.Err, width)))
		b.WriteByte('\n')
	}
	if item.Termination != "" {
		b.WriteByte('\n')
		b.WriteString(sectionStyle.Render("Termination"))
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render(wrapWorkerPlain(item.Termination, width)))
		b.WriteByte('\n')
	}
	if item.Active() && item.Result == "" {
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render("Output will appear when the worker finishes."))
	}
	return b.String()
}

func workerStateStyle(state string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return warningStyle
	case "queued":
		return dimStyle
	case "stopping":
		return selectedStyle
	case "failed", "error", "aborted":
		return errorStyle
	default:
		return statusStyle
	}
}

func compactWorkerText(s string) string {
	return strings.Join(strings.Fields(safeWorkerText(s)), " ")
}

func workerActivitySummary(activity string) string {
	prefix, _, ok := splitJSONPayload(activity)
	if ok && prefix != "" {
		return compactWorkerText(prefix)
	}
	if ok {
		return "JSON payload"
	}
	return compactWorkerText(activity)
}

func splitJSONPayload(s string) (prefix, pretty string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	for i, r := range s {
		if r != '{' && r != '[' {
			continue
		}
		pretty, ok = indentJSON(s[i:])
		if !ok {
			continue
		}
		prefix = strings.TrimSpace(s[:i])
		prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "·"))
		prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "-"))
		return prefix, pretty, true
	}
	return "", "", false
}

func indentJSON(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || (raw[0] != '{' && raw[0] != '[') {
		return "", false
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

func formatWorkerPayload(raw string, width int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	prefix, pretty, ok := splitJSONPayload(raw)
	if !ok {
		return wrapWorkerPlain(raw, width)
	}
	body := colorJSON(pretty)
	if prefix == "" {
		return ansi.Hardwrap(body, max(1, width), true)
	}
	head := dimStyle.Render(compactWorkerText(prefix))
	return head + "\n" + ansi.Hardwrap(body, max(1, width), true)
}

func wrapWorkerPlain(text string, width int) string {
	return ansi.Hardwrap(sanitizeWorkerDisplay(text), max(1, width), true)
}

func sanitizeWorkerDisplay(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == 0x1b:
			return r
		case r < 32 || r == 127:
			return -1
		default:
			return r
		}
	}, text)
}

func colorJSON(pretty string) string {
	var b strings.Builder
	expectKey := false
	depth := 0
	i := 0
	for i < len(pretty) {
		r, size := utf8.DecodeRuneInString(pretty[i:])
		switch {
		case r == '"':
			str, next := jsonStringAt(pretty, i)
			rest := strings.TrimLeftFunc(pretty[next:], unicode.IsSpace)
			if expectKey && strings.HasPrefix(rest, ":") {
				b.WriteString(jsonKeyStyle.Render(str))
			} else {
				b.WriteString(jsonStringStyle.Render(str))
			}
			i = next
			continue
		case r == '{':
			depth++
			expectKey = true
			b.WriteString(jsonPunctStyle.Render("{"))
		case r == '[':
			depth++
			expectKey = false
			b.WriteString(jsonPunctStyle.Render("["))
		case r == '}' || r == ']':
			depth = max(0, depth-1)
			expectKey = false
			b.WriteString(jsonPunctStyle.Render(string(r)))
		case r == ',':
			expectKey = depth > 0 && lastNonSpaceIsObjectComma(pretty, i)
			b.WriteString(jsonPunctStyle.Render(","))
		case r == ':':
			expectKey = false
			b.WriteString(jsonPunctStyle.Render(":"))
		case r == 't' && strings.HasPrefix(pretty[i:], "true"):
			b.WriteString(jsonBoolStyle.Render("true"))
			i += len("true")
			continue
		case r == 'f' && strings.HasPrefix(pretty[i:], "false"):
			b.WriteString(jsonBoolStyle.Render("false"))
			i += len("false")
			continue
		case r == 'n' && strings.HasPrefix(pretty[i:], "null"):
			b.WriteString(jsonNullStyle.Render("null"))
			i += len("null")
			continue
		case r == '-' || (r >= '0' && r <= '9'):
			num, next := jsonNumberAt(pretty, i)
			b.WriteString(jsonNumberStyle.Render(num))
			i = next
			continue
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

func jsonStringAt(s string, i int) (string, int) {
	if i >= len(s) || s[i] != '"' {
		return "", i
	}
	j := i + 1
	escape := false
	for j < len(s) {
		c := s[j]
		if escape {
			escape = false
			j++
			continue
		}
		if c == '\\' {
			escape = true
			j++
			continue
		}
		if c == '"' {
			return s[i : j+1], j + 1
		}
		j++
	}
	return s[i:], len(s)
}

func jsonNumberAt(s string, i int) (string, int) {
	j := i
	if j < len(s) && (s[j] == '-' || s[j] == '+') {
		j++
	}
	for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.' || s[j] == 'e' || s[j] == 'E' || s[j] == '+' || s[j] == '-') {
		j++
	}
	return s[i:j], j
}

func lastNonSpaceIsObjectComma(s string, comma int) bool {
	// After a comma, the next string is a key only inside an object.
	// Walk backward past space to find the comma's container: if the last
	// opener without a closer is '{', this comma separates object fields.
	depthObj, depthArr := 0, 0
	for i := comma - 1; i >= 0; i-- {
		switch s[i] {
		case '}':
			depthObj++
		case ']':
			depthArr++
		case '{':
			if depthObj == 0 && depthArr == 0 {
				return true
			}
			depthObj--
		case '[':
			if depthObj == 0 && depthArr == 0 {
				return false
			}
			depthArr--
		}
	}
	return false
}

var (
	jsonKeyStyle    = lipgloss.NewStyle().Foreground(colorAccent)
	jsonStringStyle = lipgloss.NewStyle().Foreground(colorAgent)
	jsonNumberStyle = lipgloss.NewStyle().Foreground(colorHearing)
	jsonBoolStyle   = lipgloss.NewStyle().Foreground(colorSpeak)
	jsonNullStyle   = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	jsonPunctStyle  = lipgloss.NewStyle().Foreground(colorDim)
	sectionStyle    = lipgloss.NewStyle().Foreground(colorDim).Bold(true)
)
