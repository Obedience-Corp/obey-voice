package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// ErrSessionNotFound is returned by Store.Delete when no session with the
// given id exists, so both the CLI and the serve handler can map it (e.g. to
// a 404) without string-matching an error message.
var ErrSessionNotFound = errors.New("session: not found")

const CurrentFormatVersion = 2

var ErrUnsupportedSessionVersion = errors.New("session: unsupported future format version")

// WriteCommittedError reports that the target file was atomically replaced,
// but the containing-directory durability check failed.
type WriteCommittedError struct{ Err error }

func (e *WriteCommittedError) Error() string {
	return "session: write committed with durability uncertainty: " + e.Err.Error()
}
func (e *WriteCommittedError) Unwrap() error { return e.Err }

// WriteCommitted distinguishes an installed file from a failed replacement.
// Callers must still surface the error: directory durability is unconfirmed.
func WriteCommitted(err error) bool {
	var e *WriteCommittedError
	return errors.As(err, &e)
}

// ErrWorkRevisionStale means a work snapshot was older than the durable
// snapshot already stored for the session.
var ErrWorkRevisionStale = errors.New("session: stale work revision")
var ErrWorkRevisionConflict = errors.New("session: conflicting work revision")

// ErrWorkScopeConflict means a snapshot attempted to replace a different
// runtime/conversation scope in the same normal session.
var ErrWorkScopeConflict = errors.New("session: work scope conflict")

// Session represents a saved conversation.
type Session struct {
	FormatVersion int          `json:"format_version,omitempty"`
	ID            string       `json:"id"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Turns         []brain.Turn `json:"turns"`
	Summary       string       `json:"summary"` // first user message, for display
	// Work is the canonical durable background-work snapshot. It is optional so
	// sessions written before work persistence was introduced remain compatible.
	Work                       *work.Snapshot `json:"work,omitempty"`
	SelectedPersonaID          string         `json:"selected_persona_id,omitempty"`
	SelectedPersonaDisplayName string         `json:"selected_persona_display_name,omitempty"`
	SelectedPersonaPromptHash  string         `json:"selected_persona_prompt_hash,omitempty"`
}

// New creates a new session with a generated ID.
func New(provider, model string) *Session {
	return &Session{
		FormatVersion: CurrentFormatVersion,
		ID:            generateID(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Provider:      provider,
		Model:         model,
	}
}

// Save persists the session to disk.
func (s *Session) Save(turns []brain.Turn) error {
	return DefaultStore().Save(s, turns)
}

// SaveBackup writes the given turns to a sibling <id>.pre-compact.json so one
// recovery generation survives /compact rewriting the live session. Each
// compact overwrites the previous backup; the main session file is untouched.
func (s *Session) SaveBackup(turns []brain.Turn) error {
	return DefaultStore().SaveBackup(s, turns)
}

func (s *Session) saveBackupTo(dir string, turns []brain.Turn) error {
	if s.FormatVersion > CurrentFormatVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedSessionVersion, s.FormatVersion)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	// Copy so the backup never mutates the live session's Turns/UpdatedAt.
	backup := *s
	backup.FormatVersion = CurrentFormatVersion
	backup.Turns = normalizeTurns(turns)
	backup.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(&backup, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session backup: %w", err)
	}
	tmp, err := os.CreateTemp(dir, s.ID+".bak-tmp-*")
	if err != nil {
		return fmt.Errorf("create backup temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close backup temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, s.ID+".pre-compact.json")); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save session backup: %w", err)
	}
	return nil
}

func (s *Session) saveTo(dir string, turns []brain.Turn) error {
	candidate := *s
	if err := saveSession(dir, &candidate, turns, nil); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// saveSession writes turns and, when supplied, a work snapshot as one normal
// session checkpoint. The private candidate is updated at the rename commit
// point. Public callers decide whether to publish it on durability errors.
func saveSession(dir string, s *Session, turns []brain.Turn, snapshot *work.Snapshot) error {
	return saveSessionWithFailures(dir, s, turns, snapshot, nil, nil)
}

func saveSessionWithFailures(dir string, s *Session, turns []brain.Turn, snapshot *work.Snapshot, beforeRename, afterRename error) error {
	next := *s
	if next.FormatVersion > CurrentFormatVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedSessionVersion, next.FormatVersion)
	}
	next.FormatVersion = CurrentFormatVersion
	next.Turns = normalizeTurns(turns)
	next.UpdatedAt = time.Now()
	if snapshot != nil {
		next.Work = cloneSnapshot(snapshot)
		next.Work.PersistedRevision = next.Work.Revision
	}

	// Set summary from first user message.
	if next.Summary == "" {
		for _, t := range next.Turns {
			if t.Role == "user" {
				next.Summary = truncate(t.Content, 80)
				break
			}
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(&next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	// Write to a temp file and rename so a crash mid-write can't corrupt an
	// existing session.
	tmp, err := os.CreateTemp(dir, next.ID+".tmp-*")
	if err != nil {
		return fmt.Errorf("create session temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close session temp file: %w", err)
	}
	if beforeRename != nil {
		_ = os.Remove(tmp.Name())
		return beforeRename
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, next.ID+".json")); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save session: %w", err)
	}
	*s = next
	if afterRename != nil {
		return &WriteCommittedError{Err: afterRename}
	}
	return syncSessionDirectory(dir)
}

// A failed directory sync cannot roll back a rename that already succeeded.
func syncSessionDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return &WriteCommittedError{Err: fmt.Errorf("open sessions directory for sync: %w", err)}
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return &WriteCommittedError{Err: fmt.Errorf("sync sessions directory: %w", err)}
	}
	return nil
}

// normalizeTurns maps provider-specific roles to the persisted scheme:
// "samantha" (claude/grok replies) is stored as "assistant".
func normalizeTurns(turns []brain.Turn) []brain.Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]brain.Turn, len(turns))
	for i, t := range turns {
		if t.Role == "samantha" {
			t.Role = "assistant"
		}
		t.ToolCalls = append([]brain.TurnToolCall(nil), t.ToolCalls...)
		out[i] = t
	}
	return out
}

// Load reads a session from disk by ID.
func Load(id string) (*Session, error) {
	return DefaultStore().Load(id)
}

func loadFrom(dir, id string) (*Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	if s.FormatVersion > CurrentFormatVersion {
		return nil, fmt.Errorf("%w: %d (current %d)", ErrUnsupportedSessionVersion, s.FormatVersion, CurrentFormatVersion)
	}
	if s.FormatVersion == 0 {
		s.FormatVersion = 1
	}
	return &s, nil
}

// Latest returns the most recently updated session, or nil if none exist.
func Latest() *Session {
	sessions := List()
	if len(sessions) == 0 {
		return nil
	}
	return &sessions[0]
}

// List returns all sessions sorted by most recently updated.
func List() []Session {
	return DefaultStore().List()
}

func listIn(dir string) []Session {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Pre-compact backups sit beside their session; they are recovery
		// artifacts, not sessions.
		if strings.HasSuffix(e.Name(), ".pre-compact.json") {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := loadFrom(dir, id)
		if err != nil {
			continue
		}
		sessions = append(sessions, *s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions
}

func generateID() string {
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:])
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func cloneSnapshot(in *work.Snapshot) *work.Snapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.Jobs = append([]work.JobView(nil), in.Jobs...)
	out.Pending = append([]work.ResultView(nil), in.Pending...)
	out.Communicated = append([]work.ResultView(nil), in.Communicated...)
	out.RetiredJobIDs = append([]string(nil), in.RetiredJobIDs...)
	for i := range out.Jobs {
		out.Jobs[i].Config = cloneStringMap(in.Jobs[i].Config)
		if in.Jobs[i].Outcome != nil {
			o := *in.Jobs[i].Outcome
			o.Config = cloneStringMap(in.Jobs[i].Outcome.Config)
			o.Limitations = in.Jobs[i].Outcome.Limitations
			out.Jobs[i].Outcome = &o
		}
	}
	for i := range out.Pending {
		out.Pending[i] = cloneWorkResult(in.Pending[i])
	}
	for i := range out.Communicated {
		out.Communicated[i] = cloneWorkResult(in.Communicated[i])
	}
	return &out
}

func cloneWorkResult(in work.ResultView) work.ResultView {
	out := in
	out.Outcome.Config = cloneStringMap(in.Outcome.Config)
	if in.LastAttempt != nil {
		e := *in.LastAttempt
		e.Receipt.Scope = in.LastAttempt.Receipt.Scope
		out.LastAttempt = &e
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Store persists sessions under an explicit directory.
//
// The package-level Save/Load/List/Latest resolve their directory from global
// config, which is correct for the CLI and wrong for a library: an embedder
// holding its own configuration must not have its sessions written to whatever
// path the process last loaded. Store is the injectable form; the package-level
// functions delegate to it so both paths share one implementation.
type Store struct {
	dir              string
	failBeforeRename error
	failAfterRename  error
}

// injectWriteFailures is a private per-store test seam.
func (st *Store) injectWriteFailures(before, after error) {
	st.failBeforeRename, st.failAfterRename = before, after
}

// SaveSelection atomically publishes the selected responder metadata while
// retaining the newest durable work snapshot. On WriteCommittedError the
// caller also receives the installed selection, with durability unconfirmed.
func (st *Store) SaveSelection(s *Session, turns []brain.Turn, personaID, displayName, promptHash string) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	current, err := loadFrom(st.dir, s.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load session before selection save: %w", err)
	}
	candidate := *s
	if err == nil {
		candidate = *current
	}
	candidate.Turns = normalizeTurns(turns)
	candidate.SelectedPersonaID = personaID
	candidate.SelectedPersonaDisplayName = displayName
	candidate.SelectedPersonaPromptHash = promptHash
	if err := saveSessionWithFailures(st.dir, &candidate, candidate.Turns, candidate.Work, st.failBeforeRename, st.failAfterRename); err != nil {
		if WriteCommitted(err) {
			*s = candidate
		}
		return err
	}
	*s = candidate
	return nil
}

// Session checkpoints are infrequent and short. A package-wide lock keeps
// independently-created Store values safe without retaining Session pointers
// or allowing path aliases to bypass a per-session lock.
var sessionWriteMu sync.Mutex

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// DefaultStore returns a Store rooted at the process-wide sessions directory.
// This is what the CLI uses, and what the package-level helpers delegate to.
func DefaultStore() *Store { return NewStore(config.SessionsDir()) }

// Save writes the session's turns while preserving the newest durable work
// snapshot. It uses the same keyed lock as SaveWork so a result arriving
// between user turns cannot be overwritten by a stale turn save.
func (st *Store) Save(s *Session, turns []brain.Turn) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	current, err := loadFrom(st.dir, s.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load session before save: %w", err)
	}
	snapshot := s.Work
	candidate := *s
	if err == nil {
		candidate.SelectedPersonaID = current.SelectedPersonaID
		candidate.SelectedPersonaDisplayName = current.SelectedPersonaDisplayName
		candidate.SelectedPersonaPromptHash = current.SelectedPersonaPromptHash
	}
	if err == nil && current.Work != nil {
		if s.Work != nil && s.Work.Scope != current.Work.Scope {
			return fmt.Errorf("%w: durable=%+v incoming=%+v", ErrWorkScopeConflict, current.Work.Scope, s.Work.Scope)
		}
		if s.Work == nil || current.Work.Revision >= s.Work.Revision {
			snapshot = current.Work
		}
	}
	if err := saveSessionWithFailures(st.dir, &candidate, turns, snapshot, st.failBeforeRename, st.failAfterRename); err != nil {
		return err
	}
	*s = candidate
	return nil
}

// SaveWork durably checkpoints canonical background-work state without
// changing the session's turns. The snapshot's persisted watermark is set to
// the revision successfully written; callers should acknowledge that revision
// in coordinator memory only after this method returns nil.
func (st *Store) SaveWork(s *Session, snapshot work.Snapshot) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	if !snapshot.Scope.Valid() || snapshot.Scope.SessionID != s.ID {
		return fmt.Errorf("%w: invalid incoming scope %+v", ErrWorkScopeConflict, snapshot.Scope)
	}
	current, err := loadFrom(st.dir, s.ID)
	if err != nil {
		return fmt.Errorf("load session for work checkpoint: %w", err)
	}
	if current.Work == nil || current.Work.Scope != snapshot.Scope {
		return fmt.Errorf("%w: durable=%+v incoming=%+v", ErrWorkScopeConflict, current.Work, snapshot.Scope)
	}
	if snapshot.Revision < current.Work.Revision {
		return fmt.Errorf("%w: durable=%d incoming=%d", ErrWorkRevisionStale, current.Work.Revision, snapshot.Revision)
	}
	if snapshot.Revision == current.Work.Revision {
		candidate := cloneSnapshot(&snapshot)
		candidate.PersistedRevision = current.Work.PersistedRevision
		// Compare the persisted representation. Runtime time.Time values carry
		// monotonic/location metadata that JSON cannot retain; an identical
		// worker checkpoint must remain idempotent after a disk round trip.
		candidateJSON, err := json.Marshal(candidate)
		if err != nil {
			return fmt.Errorf("encode work checkpoint: %w", err)
		}
		currentJSON, err := json.Marshal(current.Work)
		if err != nil {
			return fmt.Errorf("encode durable work checkpoint: %w", err)
		}
		if !bytes.Equal(candidateJSON, currentJSON) {
			return fmt.Errorf("%w: revision=%d", ErrWorkRevisionConflict, snapshot.Revision)
		}
		// A previous attempt may have renamed this exact revision and then
		// failed directory sync. Reconfirm durability before acknowledging it.
		if st.failAfterRename != nil {
			return &WriteCommittedError{Err: st.failAfterRename}
		}
		if err := syncSessionDirectory(st.dir); err != nil {
			return err
		}
		s.Work = cloneSnapshot(current.Work)
		return nil
	}
	copy := cloneSnapshot(&snapshot)
	copy.PersistedRevision = copy.Revision
	baseCopy := *s
	base := &baseCopy
	if err == nil {
		base = current
	}
	if err := saveSessionWithFailures(st.dir, base, base.Turns, copy, st.failBeforeRename, st.failAfterRename); err != nil {
		return err
	}
	s.Work = cloneSnapshot(copy)
	return nil
}

// BeginWork atomically starts a runtime/conversation scope for a normal
// session. expectedPriorScope must be nil when no work snapshot is expected,
// or point to the exact durable scope being replaced. This is the only API
// that may transition scope; SaveWork is intentionally same-scope only.
func (st *Store) BeginWork(s *Session, snapshot work.Snapshot, expectedPriorScope *work.WorkScope) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	if !snapshot.Scope.Valid() || snapshot.Scope.SessionID != s.ID {
		return fmt.Errorf("%w: invalid incoming scope %+v", ErrWorkScopeConflict, snapshot.Scope)
	}
	current, err := loadFrom(st.dir, s.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load session for work scope transition: %w", err)
	}
	var currentScope *work.WorkScope
	if err == nil && current.Work != nil {
		scope := current.Work.Scope
		currentScope = &scope
	}
	if !sameScope(currentScope, expectedPriorScope) {
		return fmt.Errorf("%w: durable=%v expected=%v", ErrWorkScopeConflict, currentScope, expectedPriorScope)
	}
	copy := cloneSnapshot(&snapshot)
	copy.PersistedRevision = copy.Revision
	baseCopy := *s
	base := &baseCopy
	if err == nil {
		base = current
	}
	if err := saveSessionWithFailures(st.dir, base, base.Turns, copy, st.failBeforeRename, st.failAfterRename); err != nil {
		return err
	}
	s.Work = cloneSnapshot(copy)
	return nil
}

func sameScope(a, b *work.WorkScope) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// SaveBackup writes a backup copy, used before a destructive operation.
func (st *Store) SaveBackup(s *Session, turns []brain.Turn) error {
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()
	return s.saveBackupTo(st.dir, turns)
}

// Load reads a session by id.
func (st *Store) Load(id string) (*Session, error) { return loadFrom(st.dir, id) }

// List returns every session in the store, newest first.
func (st *Store) List() []Session { return listIn(st.dir) }

// Latest returns the most recent session, or nil when the store is empty.
func (st *Store) Latest() *Session {
	sessions := st.List()
	if len(sessions) == 0 {
		return nil
	}
	newest := sessions[0]
	return &newest
}

// Delete removes the session file <id>.json and its pre-compact backup
// (<id>.pre-compact.json, best-effort — its absence is not an error).
// Returns ErrSessionNotFound when no session with that id exists. id is
// validated before any filesystem call: a path separator or a ".." segment
// is rejected outright, so a delete verb can never escape the store's
// directory no matter what a caller passes.
func (st *Store) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	sessionWriteMu.Lock()
	defer sessionWriteMu.Unlock()

	path := filepath.Join(st.dir, id+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("delete session %s: %w", id, err)
	}

	backup := filepath.Join(st.dir, id+".pre-compact.json")
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session %s backup: %w", id, err)
	}
	return nil
}

// validateSessionID rejects an id that could escape the store's directory
// once joined into a path: empty, containing a path separator (either OS's,
// regardless of the host), or containing ".." anywhere. Checked before any
// filesystem call, not after.
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session: empty id")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("session: invalid id %q", id)
	}
	return nil
}
