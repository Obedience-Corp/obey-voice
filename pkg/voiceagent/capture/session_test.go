package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
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

func TestCaptureUniqueIdentityAndPermissions(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	a, err := Start(Options{Root: root, SessionID: "session", Mode: ModeTalk, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Start(Options{Root: root, SessionID: "session", Mode: ModeTalk, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if a.Dir() == b.Dir() {
		t.Fatal("same-second capture reused directory")
	}
	for _, s := range []*Session{a, b} {
		if err := s.Stop(nil); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var header struct {
			TraceSchema string `json:"trace_schema"`
			Trace       struct {
				CaptureID string `json:"capture_id"`
			} `json:"trace"`
		}
		if err := json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &header); err != nil {
			t.Fatal(err)
		}
		if header.Trace.CaptureID == "" || header.Trace.CaptureID != s.manifest.CaptureID || header.TraceSchema != s.manifest.TraceSchema {
			t.Fatal("header/manifest identity disagrees")
		}
		for _, name := range []string{"events.jsonl", "manifest.json", "transcript.json"} {
			st, err := os.Stat(filepath.Join(s.Dir(), name))
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatalf("%s permission: %v %v", name, st, err)
			}
		}
	}
}

func TestCaptureSealStalledSidecarsDoNotHoldStateLock(t *testing.T) {
	for _, sidecar := range []string{"manifest", "transcript"} {
		t.Run(sidecar, func(t *testing.T) {
			s, err := Start(Options{Root: t.TempDir(), SessionID: "session", Mode: ModeTalk, Bus: events.NewBus()})
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			if sidecar == "manifest" {
				s.persistManifest = func(m Manifest) error { close(entered); <-release; return writeManifestValue(m) }
			} else {
				s.persistTurns = func(dir string, turns []Turn) error { close(entered); <-release; return writeTurns(dir, turns) }
			}
			done := s.SealAsync(nil)
			<-entered
			state := make(chan struct{})
			go func() { _ = s.Dir(); _ = s.Mode(); _ = s.Health(); close(state) }()
			select {
			case <-state:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("sidecar I/O held state mutex")
			}
			raw, err := os.ReadFile(filepath.Join(s.Dir(), "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "sealed_at") {
				t.Fatal("stalled seal claimed persistence")
			}
			close(release)
			<-done
			if err := s.SealError(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSuccessorCaptureRejectsOldScopeKeepsDelayedSameRuntime(t *testing.T) {
	root := t.TempDir()
	bus := events.NewBus()
	old := events.TraceContext{RuntimeID: "old-runtime", SessionID: "old-session", ConversationEpoch: "old-epoch"}
	first, err := Start(Options{Root: root, Bus: bus, Mode: ModeTalk, SessionID: old.SessionID, Trace: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Stop(nil); err != nil {
		t.Fatal(err)
	}
	current := events.TraceContext{RuntimeID: "new-runtime", SessionID: "new-session", ConversationEpoch: "new-epoch"}
	next, err := Start(Options{Root: root, Bus: bus, Mode: ModeTalk, SessionID: current.SessionID, Trace: current})
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range []events.TraceContext{old, {RuntimeID: current.RuntimeID, SessionID: "old-session", ConversationEpoch: current.ConversationEpoch}, {RuntimeID: current.RuntimeID, SessionID: current.SessionID, ConversationEpoch: "old-epoch"}} {
		frozen := trace
		bus.Emit(events.Error{Trace: &frozen, Stage: "late", Message: "excluded old scope"})
	}
	bus.Emit(events.WorkerUsageRecorded{Scope: work.WorkScope{RuntimeEpoch: old.RuntimeID, SessionID: old.SessionID, ConversationEpoch: old.ConversationEpoch}, JobID: "old-job"})
	delayed := current
	response := "same-runtime-old-response"
	delayed.ResponseID = &response
	bus.Emit(events.ResponseReady{Trace: &delayed, Response: "accepted delayed response"})
	bus.Emit(events.Info{Message: "accepted unattributed legacy evidence"})
	if err := next.Stop(nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(next.EventsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "excluded old scope") || strings.Contains(string(raw), "old-job") || !strings.Contains(string(raw), "accepted delayed response") || !strings.Contains(string(raw), "accepted unattributed legacy evidence") {
		t.Fatal("successor scope policy violated", string(raw))
	}
	health := next.Health()
	if health.ScopeExcluded != 4 || health.Dropped != 0 || health.Skipped != 0 {
		t.Fatal("scope exclusion mislabeled queue/payload loss", health)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(next.Dir(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Health.ScopeExcluded != 4 || manifest.Status != "sealed" {
		t.Fatal("scope exclusion absent/mislabeled in persisted manifest", manifest)
	}
}
