package log

import (
	"fmt"
	"strings"
	"time"
)

// controlSymbols is the set of client-driven event kinds AppendControl
// accepts, mapped to the marker used in the human document.
var controlSymbols = map[string]string{
	TypeBookmark:   "★",
	TypeNote:       "📝 note",
	TypePause:      "⏸ paused",
	TypeResume:     "▶ resumed",
	TypeIdeaStart:  "💡 idea start",
	TypeIdeaEnd:    "💡 idea end",
	TypeIdeaFiled:  "💡 idea filed",
	TypeSegmentGap: "⚠ audio gap",
}

// AppendControl records a client-driven event at an explicit meeting-relative
// offset. A remote recorder (phone, Watch) knows when a moment happened better
// than the server, which only learns of it after a network hop, so the offset
// is taken on trust and merely clamped to non-negative.
//
// Kinds are the additive control types plus bookmark/note, keeping the JSONL
// schema identical to a desktop recording's.
func (w *Writer) AppendControl(kind string, offsetMs int64, label, text string) error {
	symbol, ok := controlSymbols[kind]
	if !ok {
		return fmt.Errorf("meetinglog: unsupported control event %q", kind)
	}
	if offsetMs < 0 {
		offsetMs = 0
	}
	label, text = strings.TrimSpace(label), strings.TrimSpace(text)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("meetinglog: writer closed before control event %q", kind)
	}
	at := w.started.Add(time.Duration(offsetMs) * time.Millisecond)
	if err := w.writeLog(controlLine(symbol, label, text, at)); err != nil {
		return err
	}
	if err := w.writeEventAt(Event{
		Type: kind, Label: label, Text: text, TS: at.Format(time.RFC3339Nano),
	}, offsetMs); err != nil {
		return err
	}
	switch kind {
	case TypeBookmark:
		w.bookmarks++
	case TypeNote:
		w.notes++
	}
	return nil
}

// controlLine renders one control marker in the same
// "[HH:MM:SS] <marker>[: text]" shape the live recorder writes.
func controlLine(symbol, label, text string, at time.Time) string {
	line := fmt.Sprintf("[%s] %s", at.Format("15:04:05"), symbol)
	if label != "" && label != "important" {
		line += " " + strings.ToUpper(label)
	}
	if text != "" {
		line += ": " + text
	}
	return line + "\n"
}

// WriteRoutePlan records the user's start-of-meeting routing intent as a
// durable JSONL event. Delivery is checked against this on the next launch:
// route_plan without a routed event means the route never completed.
func (w *Writer) WriteRoutePlan(destID, mode string) error {
	destID = strings.TrimSpace(destID)
	if destID == "" {
		return fmt.Errorf("meetinglog: route plan destination is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("meetinglog: writer closed before route plan")
	}
	return w.writeEvent(Event{Type: TypeRoutePlan, Label: destID, Text: mode})
}
