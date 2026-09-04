package continuity

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCampProjectionKeepsIdentityBeforeLargePayload(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"items": []any{map[string]any{"id": "WI-b94dad", "title": "context continuity", "status": "active", "body": strings.Repeat("large payload ", 2000)}}}})
	note, e := ProjectToolResult(string(raw), Provenance{ToolName: "run_command"})
	for _, want := range []string{"WI-b94dad", "context continuity", "active"} {
		if !strings.Contains(note, want) {
			t.Errorf("missing %q: %s", want, note)
		}
	}
	if len(note) > NoteBytes || e.Provenance.RawBytes != len(raw) || e.Raw != string(raw) {
		t.Fatalf("invalid projection bounds/provenance: note %d raw %d", len(note), len(e.Raw))
	}
}

func TestProjectionAndArchiveStayBoundedWithInvalidUTF8(t *testing.T) {
	raw := strings.Repeat("日本語", 10000) + "\xff"
	var store Store
	for i := 0; i < 150; i++ {
		note, e := ProjectToolResult(raw, Provenance{ToolName: "worker", SourceRef: "job-1"})
		if len(note) > NoteBytes || !utf8.ValidString(note) || len(e.Raw) > RawBytes || !utf8.ValidString(e.Raw) {
			t.Fatal("invalid UTF-8 or unbounded output")
		}
		store.Add(e)
	}
	entries := store.Snapshot()
	if len(entries) != ArchiveEntries || entries[0].Kind != "turn_block" || entries[0].Raw != "" {
		t.Fatalf("archive not folded: %+v", entries[0])
	}
	if !strings.Contains(entries[49].Summary, "job-1") {
		t.Fatal("worker source lost")
	}
	entries[0].Summary = "mutated"
	if store.Snapshot()[0].Summary == "mutated" {
		t.Fatal("snapshot aliases archive")
	}
}

func TestProjectionEmptyMalformedAndDeepJSON(t *testing.T) {
	for _, raw := range []string{"", "{malformed", strings.Repeat("[", 100) + `"hello"` + strings.Repeat("]", 100)} {
		note, _ := ProjectToolResult(raw, Provenance{ToolName: "tool"})
		if len(note) == 0 || len(note) > NoteBytes {
			t.Fatalf("bad note: %q", note)
		}
	}
}
