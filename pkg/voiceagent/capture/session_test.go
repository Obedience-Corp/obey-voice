package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestStartStopRecordsErrorAndSeals(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	s, err := Start(Options{Root: root, SessionID: "sess-1", Mode: ModeTalk, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != ModeTalk {
		t.Fatalf("mode = %q", s.Mode())
	}
	bus.Emit(events.UserInput{Text: "Hey Veronica"})
	bus.Emit(events.Error{Stage: "admit", Message: "admit response: persona name \"veronica\" is ambiguous between veronica, veronica-hype"})
	if err := s.Stop([]Turn{{Role: "user", Content: "Hey Veronica"}}); err != nil {
		t.Fatal(err)
	}

	dir := s.Dir()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != Schema || m.Mode != ModeTalk || m.SealedAt == nil {
		t.Fatalf("manifest = %+v", m)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("dir mode = %v err=%v", st.Mode(), err)
	}

	eventsRaw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(eventsRaw)
	if !strings.Contains(text, `"type":"error"`) || !strings.Contains(text, "ambiguous") {
		t.Fatalf("events.jsonl missing admit error:\n%s", text)
	}
	if !strings.Contains(text, `"type":"user"`) {
		t.Fatalf("events.jsonl missing user:\n%s", text)
	}

	listed, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].SessionID != "sess-1" {
		t.Fatalf("list = %+v", listed)
	}
}

func TestAttachAllDetaches(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	s, err := Start(Options{Root: root, SessionID: "s", Mode: ModeTalk, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(nil); err != nil {
		t.Fatal(err)
	}
	bus.Emit(events.Error{Stage: "admit", Message: "after stop"})
	raw, _ := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if strings.Contains(string(raw), "after stop") {
		t.Fatalf("wrote after detach:\n%s", raw)
	}
}
