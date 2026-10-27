package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func formatWorkerDetail(item pipeline.WorkerInfo, width int) string {
	width = max(1, width)
	var b strings.Builder
	b.WriteString(normalStyle.Render(wrapWorkerPlain(item.Task, width)))
	b.WriteString("\n\n")
	b.WriteString(workerStateStyle(item.State).Render(item.State))
	b.WriteString("  ")
	b.WriteString(metaStyle.Render(workerProvider(item)))
	b.WriteString(dimStyle.Render("  " + roundSeconds(item.Elapsed)))
	b.WriteByte('\n')
	b.WriteString(dimStyle.Render("ID  " + item.ID))
	b.WriteByte('\n')
	if item.Activity != "" && len(item.Tools) == 0 {
		b.WriteByte('\n')
		b.WriteString(sectionStyle.Render("Activity"))
		b.WriteByte('\n')
		b.WriteString(formatWorkerPayload(item.Activity, width))
		b.WriteByte('\n')
	}
	if len(item.Tools) > 0 {
		b.WriteString("\n")
		b.WriteString(sectionStyle.Render("Tool activity (newest first)"))
		b.WriteByte('\n')
		for i := len(item.Tools) - 1; i >= 0; i-- {
			call := item.Tools[i]
			elapsed := call.Elapsed
			if call.Status == "running" && !call.Started.IsZero() {
				elapsed = time.Since(call.Started)
			}
			b.WriteString(wrapWorkerPlain(fmt.Sprintf("%d. %s · %s · %s", i+1, call.Name, call.Status, roundSeconds(elapsed)), width))
			b.WriteByte('\n')
			if call.Summary != "" {
				b.WriteString(wrapWorkerPlain(call.Summary, width))
				b.WriteByte('\n')
			}
			if call.Preview != "" {
				b.WriteString(formatWorkerPayload(call.Preview, width))
				b.WriteByte('\n')
			}
			if call.Err != "" {
				b.WriteString(errorStyle.Render(wrapWorkerPlain(call.Err, width)))
				b.WriteByte('\n')
			}
			b.WriteByte('\n')
		}
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
		b.WriteString(dimStyle.Render(wrapWorkerPlain("Output will appear when the worker finishes.", width)))
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

func workerCurrentActivity(item pipeline.WorkerInfo) string {
	for i := len(item.Tools) - 1; i >= 0; i-- {
		call := item.Tools[i]
		if call.Status == "running" {
			return compactWorkerText(call.Name + " · " + call.Summary)
		}
	}
	if len(item.Tools) > 0 && item.Active() {
		return "Waiting for next worker update"
	}
	if item.Activity == "" && item.State == "running" {
		return "Waiting for first tool activity"
	}
	return workerActivitySummary(item.Activity)
}

func workerActivitySummary(activity string) string {
	prefix, _, found := splitJSONLead(activity)
	if !found {
		return compactWorkerText(activity)
	}
	if prefix != "" {
		return compactWorkerText(prefix)
	}
	return "JSON payload"
}

// splitJSONLead cuts at the first JSON object/array opener. The remainder
// need not be valid JSON: production Activity is a 240-rune tool preview.
func splitJSONLead(s string) (prefix, rest string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	for i, r := range s {
		if r != '{' && r != '[' {
			continue
		}
		prefix = strings.TrimSpace(s[:i])
		prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "·"))
		prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "-"))
		return prefix, s[i:], true
	}
	return "", "", false
}

func splitJSONPayload(s string) (prefix, pretty string, ok bool) {
	prefix, rest, found := splitJSONLead(s)
	if !found {
		return "", "", false
	}
	pretty, ok = indentJSON(rest)
	if !ok {
		return "", "", false
	}
	return prefix, pretty, true
}

func indentJSON(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || (raw[0] != '{' && raw[0] != '[') {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// prettyJSONPrefix lays out a compact JSON prefix without requiring a complete
// value, so a 240-rune tool preview still gets keys on their own lines.
func prettyJSONPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	depth := 0
	inStr := false
	esc := false
	needIndent := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case ' ', '\n', '\t', '\r':
			continue
		case '"':
			if needIndent {
				writeJSONIndent(&b, depth)
				needIndent = false
			}
			inStr = true
			b.WriteByte(c)
		case '{', '[':
			if needIndent {
				writeJSONIndent(&b, depth)
			}
			b.WriteByte(c)
			depth++
			needIndent = true
		case '}', ']':
			depth = max(0, depth-1)
			writeJSONIndent(&b, depth)
			b.WriteByte(c)
			needIndent = false
		case ',':
			b.WriteByte(c)
			needIndent = true
		case ':':
			b.WriteString(": ")
		default:
			if needIndent {
				writeJSONIndent(&b, depth)
				needIndent = false
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

func writeJSONIndent(b *strings.Builder, depth int) {
	b.WriteByte('\n')
	for range depth {
		b.WriteString("  ")
	}
}

func formatWorkerPayload(raw string, width int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	width = max(1, width)
	prefix, rest, found := splitJSONLead(raw)
	if !found {
		return wrapWorkerPlain(raw, width)
	}
	stripped := ansi.Strip(rest)
	pretty, ok := indentJSON(stripped)
	if !ok {
		pretty = prettyJSONPrefix(stripped)
	}
	body := ansi.Hardwrap(colorJSON(pretty), width, true)
	if prefix == "" {
		return body
	}
	return dimStyle.Render(compactWorkerText(prefix)) + "\n" + body
}

func wrapWorkerPlain(text string, width int) string {
	return ansi.Hardwrap(safeWorkerText(text), max(1, width), true)
}

func colorJSON(pretty string) string {
	var b strings.Builder
	inObject := []bool{}
	expectKey := false
	i := 0
	for i < len(pretty) {
		r, size := utf8.DecodeRuneInString(pretty[i:])
		switch {
		case r == '"':
			str, next := jsonStringAt(pretty, i)
			if expectKey {
				b.WriteString(jsonKeyStyle.Render(str))
			} else {
				b.WriteString(jsonStringStyle.Render(str))
			}
			i = next
			continue
		case r == '{':
			inObject = append(inObject, true)
			expectKey = true
			b.WriteString(jsonPunctStyle.Render("{"))
		case r == '[':
			inObject = append(inObject, false)
			expectKey = false
			b.WriteString(jsonPunctStyle.Render("["))
		case r == '}':
			if n := len(inObject); n > 0 {
				inObject = inObject[:n-1]
			}
			expectKey = false
			b.WriteString(jsonPunctStyle.Render("}"))
		case r == ']':
			if n := len(inObject); n > 0 {
				inObject = inObject[:n-1]
			}
			expectKey = false
			b.WriteString(jsonPunctStyle.Render("]"))
		case r == ',':
			expectKey = len(inObject) > 0 && inObject[len(inObject)-1]
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

var (
	jsonKeyStyle    = lipgloss.NewStyle().Foreground(colorAccent)
	jsonStringStyle = lipgloss.NewStyle().Foreground(colorAgent)
	jsonNumberStyle = lipgloss.NewStyle().Foreground(colorHearing)
	jsonBoolStyle   = lipgloss.NewStyle().Foreground(colorSpeak)
	jsonNullStyle   = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	jsonPunctStyle  = lipgloss.NewStyle().Foreground(colorDim)
	sectionStyle    = lipgloss.NewStyle().Foreground(colorDim).Bold(true)
)
