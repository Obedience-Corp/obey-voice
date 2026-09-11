package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		turns []brain.Turn
		want  []brain.Turn
	}{
		{
			name:  "empty turns",
			turns: nil,
			want:  nil,
		},
		{
			name: "samantha role persisted as assistant",
			turns: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "samantha", Content: "hello"},
			},
			want: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
		},
		{
			name: "ollama roles pass through",
			turns: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "calling a tool"},
				{Role: "tool", Content: "result"},
				{Role: "assistant", Content: "done"},
			},
			want: []brain.Turn{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "calling a tool"},
				{Role: "tool", Content: "result"},
				{Role: "assistant", Content: "done"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := New("claude", "claude")
			if err := s.saveTo(dir, tt.turns); err != nil {
				t.Fatalf("saveTo() error = %v", err)
			}

			got, err := loadFrom(dir, s.ID)
			if err != nil {
				t.Fatalf("loadFrom() error = %v", err)
			}
			if !reflect.DeepEqual(got.Turns, tt.want) {
				t.Errorf("Turns = %+v, want %+v", got.Turns, tt.want)
			}
			if got.ID != s.ID || got.Provider != "claude" || got.Model != "claude" {
				t.Errorf("metadata = (%s, %s, %s), want (%s, claude, claude)", got.ID, got.Provider, got.Model, s.ID)
			}
		})
	}
}

func testSnapshot(revision uint64, scope work.WorkScope) work.Snapshot {
	return work.Snapshot{
		Scope: scope, Revision: revision,
		Jobs:    []work.JobView{{Key: work.JobKey{Scope: scope, JobID: "job-1"}, Task: "weather", Origin: "user", State: work.JobTerminal}},
		Pending: []work.ResultView{{State: work.Held, Outcome: work.Outcome{ResultID: "result-1", Scope: scope, JobID: "job-1", Status: work.OutcomeSucceeded, Text: "sunny", PreviousSession: true}}},
	}
}

func TestSaveWorkRoundTripAndPreservesTurns(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	turns := []brain.Turn{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}
	if err := store.Save(s, turns); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-1", ConversationEpoch: "conversation-1"}
	snap := testSnapshot(2, scope)
	if err := store.BeginWork(s, snap, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Turns, turns) {
		t.Fatalf("turns changed during work save: got %+v want %+v", got.Turns, turns)
	}
	if got.Work == nil || got.Work.Revision != 2 || got.Work.PersistedRevision != 2 || got.Work.Scope != scope {
		t.Fatalf("work snapshot = %+v", got.Work)
	}
}

func TestWorkSnapshotRetiredIDsAreCopiedAtSessionBoundaries(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	if err := store.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-copy", ConversationEpoch: "conversation-copy"}
	snap := testSnapshot(1, scope)
	snap.RetiredJobIDs = []string{"retired-1"}
	if err := store.BeginWork(s, snap, nil); err != nil {
		t.Fatal(err)
	}
	snap.RetiredJobIDs[0] = "caller-mutated"
	if s.Work == nil || len(s.Work.RetiredJobIDs) != 1 || s.Work.RetiredJobIDs[0] != "retired-1" {
		t.Fatalf("in-memory snapshot aliased caller: %+v", s.Work)
	}
	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Work == nil || loaded.Work.RetiredJobIDs[0] != "retired-1" {
		t.Fatalf("durable snapshot aliased caller: %+v", loaded.Work)
	}

	next := testSnapshot(2, scope)
	next.RetiredJobIDs = []string{"retired-2"}
	if err := store.SaveWork(s, next); err != nil {
		t.Fatal(err)
	}
	next.RetiredJobIDs[0] = "caller-mutated-again"
	if s.Work.RetiredJobIDs[0] != "retired-2" {
		t.Fatalf("SaveWork mutated in-memory snapshot: %+v", s.Work)
	}
	loaded, err = store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Work.RetiredJobIDs[0] != "retired-2" {
		t.Fatalf("SaveWork mutated durable snapshot: %+v", loaded.Work)
	}
}

func TestSaveWorkRejectsStaleRevisionAndScope(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	if err := store.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-1", ConversationEpoch: "conversation-1"}
	if err := store.BeginWork(s, testSnapshot(3, scope), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWork(s, testSnapshot(2, scope)); !errors.Is(err, ErrWorkRevisionStale) {
		t.Fatalf("stale SaveWork error = %v", err)
	}
	other := scope
	other.ConversationEpoch = "conversation-2"
	if err := store.SaveWork(s, testSnapshot(4, other)); !errors.Is(err, ErrWorkScopeConflict) {
		t.Fatalf("scope SaveWork error = %v", err)
	}
	got, err := store.Load(s.ID)
	if err != nil || got.Work.Revision != 3 {
		t.Fatalf("durable snapshot changed after rejected writes: got=%+v err=%v", got.Work, err)
	}
}

func TestBeginWorkScopeCASAndRejectsLateOldWrite(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatal(err)
	}
	old := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-1", ConversationEpoch: "conversation-1"}
	if err := store.BeginWork(s, testSnapshot(4, old), nil); err != nil {
		t.Fatal(err)
	}
	next := old
	next.RuntimeEpoch = "runtime-2"
	next.ConversationEpoch = "conversation-2"
	if err := store.BeginWork(s, testSnapshot(1, next), &old); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWork(s, testSnapshot(99, old)); !errors.Is(err, ErrWorkScopeConflict) {
		t.Fatalf("late old SaveWork error = %v", err)
	}
	wrongPrior := old
	wrongPrior.RuntimeEpoch = "runtime-wrong"
	if err := store.BeginWork(s, testSnapshot(2, old), &wrongPrior); !errors.Is(err, ErrWorkScopeConflict) {
		t.Fatalf("failed CAS error = %v", err)
	}
	got, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Work.Scope != next || got.Work.Revision != 1 {
		t.Fatalf("failed transition changed durable work: %+v", got.Work)
	}
}

func TestBeginWorkCreatesNewSessionFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("ollama", "model")
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-new", ConversationEpoch: "conversation-new"}
	if err := store.BeginWork(s, testSnapshot(1, scope), nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(s.ID)
	if err != nil || got.Work == nil || got.Work.Scope != scope {
		t.Fatalf("new session work = %+v err=%v", got, err)
	}
}

func TestSaveWorkSameRevisionConflictLeavesFileUnchanged(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	if err := store.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-1", ConversationEpoch: "conversation-1"}
	first := testSnapshot(1, scope)
	if err := store.BeginWork(s, first, nil); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Pending[0].Outcome.Text = "different"
	if err := store.SaveWork(s, conflict); !errors.Is(err, ErrWorkRevisionConflict) {
		t.Fatalf("same-revision conflict error = %v", err)
	}
	got, err := store.Load(s.ID)
	if err != nil || got.Work.Pending[0].Outcome.Text != "sunny" {
		t.Fatalf("same-revision conflict changed file: %+v err=%v", got.Work, err)
	}
}

func TestSaveCorruptSessionReturnsErrorAndLeavesFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("ollama", "model")
	path := filepath.Join(dir, s.ID+".json")
	const corrupt = "{not valid json"
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "do not overwrite"}}); err == nil {
		t.Fatal("Save() on corrupt session returned nil")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != corrupt {
		t.Fatalf("corrupt session changed: %q err=%v", got, err)
	}
}

func TestConcurrentTurnAndWorkSavesPreserveBoth(t *testing.T) {
	store := NewStore(t.TempDir())
	s := New("ollama", "model")
	base := []brain.Turn{{Role: "user", Content: "first"}}
	if err := store.Save(s, base); err != nil {
		t.Fatal(err)
	}
	scope := work.WorkScope{SessionID: s.ID, RuntimeEpoch: "runtime-1", ConversationEpoch: "conversation-1"}
	turns := append(append([]brain.Turn(nil), base...), brain.Turn{Role: "assistant", Content: "second"})
	snapshot := testSnapshot(2, scope)
	if err := store.BeginWork(s, testSnapshot(1, scope), nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var saveErr, workErr error
	wg.Add(2)
	go func() { defer wg.Done(); saveErr = store.Save(s, turns) }()
	go func() { defer wg.Done(); workErr = store.SaveWork(s, snapshot) }()
	wg.Wait()
	if saveErr != nil || workErr != nil {
		t.Fatalf("concurrent saves failed: turn=%v work=%v", saveErr, workErr)
	}
	got, err := store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 || got.Turns[1].Content != "second" {
		t.Fatalf("turn history lost: %+v", got.Turns)
	}
	if got.Work == nil || got.Work.Revision != 2 {
		t.Fatalf("work snapshot lost: %+v", got.Work)
	}
}

func TestLoadMissingSession(t *testing.T) {
	if _, err := loadFrom(t.TempDir(), "nope"); err == nil {
		t.Fatal("loadFrom() = nil error for missing session")
	}
}

func TestGenerateIDUniqueAcrossRapidCalls(t *testing.T) {
	format := regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)
	seen := make(map[string]bool)
	for range 4 {
		id := generateID()
		if !format.MatchString(id) {
			t.Fatalf("generateID() = %q, want timestamp with 4-hex suffix", id)
		}
		if seen[id] {
			t.Fatalf("generateID() produced duplicate %q", id)
		}
		seen[id] = true
	}
}

func TestListOrderingAndCorruptSkip(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var ids []string
	for i := range 3 {
		s := New("ollama", "m")
		turns := []brain.Turn{{Role: "user", Content: fmt.Sprintf("msg %d", i)}}
		if err := s.saveTo(dir, turns); err != nil {
			t.Fatalf("saveTo() error = %v", err)
		}
		ids = append(ids, s.ID)
		time.Sleep(2 * time.Millisecond) // distinct UpdatedAt
	}

	sessions := listIn(dir)
	if len(sessions) != 3 {
		t.Fatalf("len(listIn()) = %d, want 3 (corrupt/non-json files skipped)", len(sessions))
	}
	for i, want := range []string{ids[2], ids[1], ids[0]} {
		if sessions[i].ID != want {
			t.Errorf("sessions[%d].ID = %s, want %s (most recent first)", i, sessions[i].ID, want)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s after save", e.Name())
		}
	}
}

func TestSaveOverwritesExistingSessionAtomically(t *testing.T) {
	dir := t.TempDir()
	s := New("ollama", "m")

	if err := s.saveTo(dir, []brain.Turn{{Role: "user", Content: "first"}}); err != nil {
		t.Fatalf("saveTo() error = %v", err)
	}
	if err := s.saveTo(dir, []brain.Turn{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
	}); err != nil {
		t.Fatalf("saveTo() error = %v", err)
	}

	got, err := loadFrom(dir, s.ID)
	if err != nil {
		t.Fatalf("loadFrom() error = %v", err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("len(Turns) = %d, want 2", len(got.Turns))
	}
	if got.Summary != "first" {
		t.Errorf("Summary = %q, want %q", got.Summary, "first")
	}
}

func TestSaveBackupWritesSiblingWithoutMutatingSession(t *testing.T) {
	dir := t.TempDir()
	s := New("claude", "sonnet")
	live := []brain.Turn{{Role: "user", Content: "live"}}
	if err := s.saveTo(dir, live); err != nil {
		t.Fatalf("saveTo() error: %v", err)
	}

	preCompact := []brain.Turn{
		{Role: "user", Content: "old one"},
		{Role: "samantha", Content: "old reply"},
	}
	if err := s.saveBackupTo(dir, preCompact); err != nil {
		t.Fatalf("saveBackupTo() error: %v", err)
	}

	// The live session's in-memory turns are untouched by the backup.
	if len(s.Turns) != 1 || s.Turns[0].Content != "live" {
		t.Fatalf("session turns mutated by backup: %+v", s.Turns)
	}

	// The backup file exists, holds the pre-compact turns (normalized), and
	// does not appear in the session listing.
	data, err := os.ReadFile(filepath.Join(dir, s.ID+".pre-compact.json"))
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	var backup Session
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	if len(backup.Turns) != 2 || backup.Turns[1].Role != "assistant" {
		t.Fatalf("backup turns = %+v, want 2 normalized turns", backup.Turns)
	}

	sessions := listIn(dir)
	if len(sessions) != 1 || sessions[0].ID != s.ID {
		t.Fatalf("listIn = %d sessions, want only the live session", len(sessions))
	}
}

// --- Store.Delete (SES-A1) ---

func TestStoreDeleteRemovesSessionAndBackup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("claude", "sonnet")
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := s.saveBackupTo(dir, []brain.Turn{{Role: "user", Content: "old"}}); err != nil {
		t.Fatalf("saveBackupTo() error = %v", err)
	}

	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, s.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("session file still exists after Delete: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, s.ID+".pre-compact.json")); !os.IsNotExist(err) {
		t.Fatalf("pre-compact backup still exists after Delete: err=%v", err)
	}
}

func TestStoreDeleteWithoutBackupSucceeds(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("claude", "sonnet")
	if err := store.Save(s, nil); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// No .pre-compact.json was ever written for this session; its absence
	// during the backup-removal step must not turn into an error.
	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil (missing backup is fine)", err)
	}
}

func TestStoreDeleteUnknownIDReturnsSentinel(t *testing.T) {
	store := NewStore(t.TempDir())
	err := store.Delete("20260101-000000-dead")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Delete() error = %v, want ErrSessionNotFound", err)
	}
}

func TestStoreDeleteRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	// A file just outside dir that a naive join could otherwise reach.
	outside := filepath.Join(filepath.Dir(dir), "outside-victim.json")
	if err := os.WriteFile(outside, []byte("do-not-touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	store := NewStore(dir)
	cases := []string{
		"../outside-victim",
		"..",
		"foo/../../outside-victim",
		"/etc/passwd",
		"a/b",
		`a\b`,
		"",
	}
	for _, id := range cases {
		err := store.Delete(id)
		if err == nil {
			t.Errorf("Delete(%q) error = nil, want a rejection", id)
		}
		if errors.Is(err, ErrSessionNotFound) {
			// A rejected id must be its own error, not indistinguishable
			// from "well-formed id, nothing there".
			t.Errorf("Delete(%q) returned ErrSessionNotFound, want a distinct invalid-id error", id)
		}
	}

	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the store directory was affected: %v", err)
	}
}

func TestStoreDeleteIsomorphicWithListedID(t *testing.T) {
	// Guards the id shape delete callers actually pass: a real generated id
	// round-trips through Save -> List -> Delete cleanly.
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("ollama", "qwen3:8b")
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`).MatchString(s.ID) {
		t.Fatalf("generated id %q does not match the documented id shape", s.ID)
	}
	if err := store.Delete(s.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("List() = %+v after Delete, want empty", got)
	}
}

// --- brain.Turn.At round-trip (SES-A5) ---

// A session file written before per-turn stamping landed has no "at" key at
// all; it must still decode cleanly, with At left at its zero value.
func TestLoadOldSessionWithoutAtKeyDecodes(t *testing.T) {
	dir := t.TempDir()
	const raw = `{
  "id": "20260101-000000-aaaa",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:05Z",
  "provider": "ollama",
  "model": "qwen3:8b",
  "turns": [{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}],
  "summary": "hi"
}`
	if err := os.WriteFile(filepath.Join(dir, "20260101-000000-aaaa.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(dir).Load("20260101-000000-aaaa")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(s.Turns) != 2 {
		t.Fatalf("turns = %+v, want 2", s.Turns)
	}
	for i, turn := range s.Turns {
		if !turn.At.IsZero() {
			t.Errorf("turn[%d].At = %v, want the zero value (no \"at\" key on disk)", i, turn.At)
		}
	}
}

// A turn with a real At round-trips through Save/Load unchanged.
func TestSaveLoadRoundTripPreservesAt(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	s := New("ollama", "qwen3:8b")
	stamped := time.Date(2026, 8, 16, 23, 14, 55, 0, time.UTC)
	if err := store.Save(s, []brain.Turn{{Role: "user", Content: "hi", At: stamped}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Turns) != 1 || !loaded.Turns[0].At.Equal(stamped) {
		t.Fatalf("loaded turns = %+v, want At = %v", loaded.Turns, stamped)
	}
}
