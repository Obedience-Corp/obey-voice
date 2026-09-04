// Package continuity bounds model-visible tool results and keeps a session-local
// archive. Projections are data, never instructions or evidence of user approval.
package continuity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const NoteBytes = 2048
const RawBytes = 32 * 1024
const ArchiveEntries = 50

type Provenance struct {
	ToolName  string
	SourceRef string
	RawBytes  int
}

type Entry struct {
	ID         string
	Kind       string
	Summary    string
	Raw        string
	Provenance Provenance
}

// Clip preserves valid UTF-8 while enforcing a byte limit.
func Clip(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	marker := "…"
	if limit < len(marker) {
		return ""
	}
	end := limit - len(marker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}

// ProjectToolResult extracts identifying JSON fields before bulky details. It
// does no network/model work and never copies more than NoteBytes to hot context.
// SourceRef carries a worker job ID or other explicit provenance, never argv.
func ProjectToolResult(raw string, p Provenance) (string, Entry) {
	p.RawBytes = len(raw)
	p.ToolName = Clip(strings.Join(strings.Fields(p.ToolName), " "), 80)
	p.SourceRef = Clip(strings.Join(strings.Fields(p.SourceRef), " "), 100)
	header := "[tool:" + p.ToolName + "]"
	if p.SourceRef != "" {
		header += " source=" + p.SourceRef
	}
	body := strings.TrimSpace(raw)
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		var facts []string
		projectJSON(value, &facts, 0)
		if len(facts) > 0 {
			body = strings.Join(facts, "\n")
		}
	}
	if body == "" {
		body = "(empty)"
	}
	note := Clip(header+"\n"+body, NoteBytes)
	return note, Entry{Kind: "tool_projection", Summary: note, Raw: Clip(raw, RawBytes), Provenance: p}
}

func projectJSON(v any, facts *[]string, depth int) {
	if depth > 16 || len(*facts) >= 80 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		// Stable identifiers and lifecycle fields must precede large descriptions.
		priority := []string{"id", "ref", "workitem_id", "job_id", "title", "name", "status", "state", "type", "path", "error", "summary"}
		used := map[string]bool{}
		var row []string
		for _, key := range priority {
			if val, ok := x[key]; ok {
				switch val.(type) {
				case string, float64, bool, nil:
					row = append(row, key+"="+Clip(fmt.Sprint(val), 300))
					used[key] = true
				}
			}
		}
		if len(row) > 0 {
			*facts = append(*facts, strings.Join(row, "; "))
		}
		keys := make([]string, 0, len(x))
		for key := range x {
			if !used[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		// Containers first: nested workitem identities precede arbitrary payload text.
		for _, key := range keys {
			switch x[key].(type) {
			case map[string]any, []any:
				projectJSON(x[key], facts, depth+1)
			}
		}
		for _, key := range keys {
			switch x[key].(type) {
			case string, float64, bool:
				if len(*facts) < 80 {
					*facts = append(*facts, key+"="+Clip(fmt.Sprint(x[key]), 300))
				}
			}
		}
	case []any:
		for _, item := range x {
			projectJSON(item, facts, depth+1)
		}
	case string:
		*facts = append(*facts, Clip(x, 300))
	}
}

// Store is owned by the provider's serialized conversation loop. Raw output is
// memory-only. Snapshot returns a copy so callers cannot mutate the ring.
type Store struct {
	entries  []Entry
	sequence uint64
}

func (s *Store) Add(e Entry) {
	s.sequence++
	e.ID = fmt.Sprintf("context-%d", s.sequence)
	e.Summary = Clip(e.Summary, NoteBytes)
	e.Raw = Clip(e.Raw, RawBytes)
	if len(s.entries) >= ArchiveEntries {
		// Fold the oldest two summaries, discard their raw payload, then free a slot.
		first := s.entries[0]
		first.Kind = "turn_block"
		first.Raw = ""
		first.Summary = Clip(first.Summary, NoteBytes/2) + "\n" + Clip(s.entries[1].Summary, NoteBytes/2-1)
		s.entries = append([]Entry{first}, s.entries[2:]...)
	}
	s.entries = append(s.entries, e)
}
func (s *Store) Snapshot() []Entry { return append([]Entry(nil), s.entries...) }

// NextContext follows the bounded ladder without shrinking explicit settings.
func NextContext(current, ceiling int) int {
	ceiling = min(ceiling, 32768)
	for _, n := range []int{8192, 16384, 32768} {
		if n > current && n <= ceiling {
			return n
		}
	}
	return current
}
