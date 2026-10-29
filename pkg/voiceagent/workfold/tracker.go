// Package workfold folds a background job's per-call tool events into the
// grouped rows a listener can follow: eleven read_file calls are one "read 11
// files", not eleven lines. The bus stays per-call (events.ToolCallStarted /
// ToolCallFinished); grouping is a view concern, and every view — TUI, stdout,
// speech — folds with this one Tracker so they cannot disagree.
package workfold

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Kind strings mirror brain.ToolKind (Grok's snake_case names). They are
// duplicated rather than imported so this package sits directly above events
// and a UI folding rows never pulls in a brain.
const (
	kindRead      = "read"
	kindListDir   = "list_dir"
	kindWrite     = "write"
	kindExecute   = "execute"
	kindWebSearch = "web_search"
	kindWebFetch  = "web_fetch"
	kindSkill     = "skill"
	kindTask      = "task"
)

// Row is one run of consecutive same-kind tool calls in a job.
type Row struct {
	Kind string
	// Count is the terminal call count. Started is retained for active display.
	Count   int
	Started int
	// Active is the number of calls whose terminal hook has not arrived.
	Active int
	// Succeeded, Failed, and Unknown are terminal observations. Unknown is
	// deliberately separate from success: a missing end hook cannot prove it.
	Succeeded int
	Failed    int
	Unknown   int
	// ReconciledUnknown is a missing terminal hook discovered when a job closes.
	ReconciledUnknown int
	Elapsed           time.Duration
	// Last is the newest finished call's preview, or its error line when the
	// call failed without one.
	Last string
}

// Counts is the terminal/active view of a job's calls.
type Counts struct {
	Active, Completed, Succeeded, Failed, Unknown int
}

// Call is a tool's observed activity, retained for the worker detail view.
// Running is distinct from unknown: a missing finish never implies success.
type Call struct {
	ID, Name, Summary, Status, Preview, Err string
	Started                                 time.Time
	Elapsed                                 time.Duration
}

// phrasing is how a kind reads out loud: "read" + "3 files".
type phrasing struct{ verb, one, many string }

var phrasings = map[string]phrasing{
	kindRead:      {"read", "file", "files"},
	kindListDir:   {"listed", "folder", "folders"},
	kindWrite:     {"wrote", "file", "files"},
	kindExecute:   {"ran", "command", "commands"},
	kindWebSearch: {"searched", "time", "times"},
	kindWebFetch:  {"fetched", "page", "pages"},
	kindSkill:     {"loaded", "skill", "skills"},
	kindTask:      {"delegated", "task", "tasks"},
}

// String renders the row as a human phrase: "read 3 files (1 failed)".
func (r Row) String() string {
	p, ok := phrasings[r.Kind]
	if !ok {
		p = phrasing{one: "tool call", many: "tool calls"}
	}
	noun := p.many
	if r.Count == 1 || (r.Count == 0 && r.Active == 1) {
		noun = p.one
	}
	var b strings.Builder
	if r.Count == 0 && r.Active > 0 {
		b.WriteString(strconv.Itoa(r.Active))
		b.WriteString(" ")
		b.WriteString(noun)
		b.WriteString(" running")
		return b.String()
	}
	if p.verb != "" {
		b.WriteString(p.verb)
		b.WriteString(" ")
	}
	b.WriteString(strconv.Itoa(r.Count))
	b.WriteString(" ")
	b.WriteString(noun)
	parts := make([]string, 0, 4)
	if r.Succeeded > 0 {
		parts = append(parts, strconv.Itoa(r.Succeeded)+" succeeded")
	}
	if r.Failed > 0 {
		parts = append(parts, strconv.Itoa(r.Failed)+" failed")
	}
	if r.Unknown > 0 {
		parts = append(parts, strconv.Itoa(r.Unknown)+" unknown")
	}
	if r.Active > 0 {
		parts = append(parts, strconv.Itoa(r.Active)+" active")
	}
	if len(parts) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	return b.String()
}

// Tracker folds tool events into rows per job. Safe for concurrent use: worker
// goroutines feed it while UI goroutines read it.
type Tracker struct {
	mu   sync.Mutex
	jobs map[string]*fold
}

// fold is one job's rows plus the bookkeeping to attribute a finish.
type fold struct {
	rows      []Row
	calls     []Call
	callIndex map[string]int
	closed    bool
	// open is the index of the row still accepting calls, or -1.
	open int
	// byCall maps a call id to the row it opened, so a finish lands on the
	// right row even if a later kind has already opened another.
	byCall   map[string]int
	finished map[string]bool
}

// New constructs an empty Tracker.
func New() *Tracker { return &Tracker{jobs: make(map[string]*fold)} }

// Add folds a started call into the job's open row, or opens a new row when
// the kind changed. The closed row is returned so a line-oriented view can
// print it exactly once. Calls with no job id are the live brain's and are
// ignored.
func (t *Tracker) Add(e events.ToolCallStarted) *Row {
	if t == nil || e.JobID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.foldFor(e.JobID)
	if f.closed {
		return nil
	}
	if e.ID != "" {
		if _, exists := f.byCall[e.ID]; exists || f.finished[e.ID] {
			return nil
		}
	}
	var closed *Row
	if f.open >= 0 && f.rows[f.open].Kind == e.Kind {
		f.rows[f.open].Started++
		f.rows[f.open].Active++
	} else {
		if f.open >= 0 {
			row := f.rows[f.open]
			closed = &row
		}
		f.rows = append(f.rows, Row{Kind: e.Kind, Started: 1, Active: 1})
		f.open = len(f.rows) - 1
	}
	if e.ID != "" {
		f.byCall[e.ID] = f.open
		f.callIndex[e.ID] = len(f.calls)
	}
	f.calls = append(f.calls, Call{ID: e.ID, Name: e.Name, Summary: e.Summary, Status: "running", Started: time.Now()})
	return closed
}

// Finish records a call's outcome on the row it belongs to.
func (t *Tracker) Finish(e events.ToolCallFinished) {
	if t == nil || e.JobID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.jobs[e.JobID]
	if f == nil || f.closed {
		return
	}
	idx, ok := f.byCall[e.ID]
	if !ok {
		// Never guess a non-empty ID onto the latest same-kind row: that can
		// close a different call. Missing IDs are reconciled as unknown at job
		// close unless an exact identity was observed.
		return
	}
	if e.ID != "" {
		delete(f.byCall, e.ID)
		f.finished[e.ID] = true
	}
	row := &f.rows[idx]
	if row.Active > 0 {
		row.Active--
	}
	switch e.OutcomeStatus() {
	case events.ToolOutcomeSucceeded:
		row.Succeeded++
	case events.ToolOutcomeFailed, events.ToolOutcomeTimedOut, events.ToolOutcomeCanceled:
		row.Failed++
	default:
		row.Unknown++
	}
	row.Count++
	row.Elapsed += e.ResultElapsed()
	if callIndex, ok := f.callIndex[e.ID]; ok {
		call := &f.calls[callIndex]
		call.Status = string(e.OutcomeStatus())
		call.Preview, call.Err, call.Elapsed = e.ResultPreview(), e.ResultReason(), e.ResultElapsed()
	}
	if preview := e.ResultPreview(); preview != "" {
		row.Last = preview
	} else if reason := e.ResultReason(); reason != "" {
		row.Last = reason
	}
}

// Calls copies per-tool details in start order. Callers may freely modify them.
func (t *Tracker) Calls(jobID string) []Call {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if f := t.jobs[jobID]; f != nil {
		return append([]Call(nil), f.calls...)
	}
	return nil
}

// Rows copies the job's rows in order.
func (t *Tracker) Rows(jobID string) []Row {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.jobs[jobID]
	if f == nil || len(f.rows) == 0 {
		return nil
	}
	return append([]Row(nil), f.rows...)
}

// Summary is the job's rows as one spoken-and-printed phrase, empty when the
// job has done nothing yet.
func (t *Tracker) Summary(jobID string) string {
	return RowsSummary(t.Rows(jobID))
}

// RowsSummary renders rows returned by Close after terminal reconciliation.
func RowsSummary(rows []Row) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.String())
	}
	return strings.Join(parts, " · ")
}

// Counts reports the calls in a job without collapsing unknown into success.
func (t *Tracker) Counts(jobID string) Counts {
	var out Counts
	for _, row := range t.Rows(jobID) {
		out.Active += row.Active
		out.Completed += row.Count
		out.Succeeded += row.Succeeded
		out.Failed += row.Failed
		out.Unknown += row.Unknown
	}
	return out
}

// Close ends the job's open row, returns every row, and forgets the job.
func (t *Tracker) Close(jobID string) []Row {
	return t.close(jobID, true)
}

// Seal reconciles a finished job once, retaining its rows and calls for
// inspection alongside the job table's completed result. Late hooks are ignored.
func (t *Tracker) Seal(jobID string) []Row {
	return t.close(jobID, false)
}

func (t *Tracker) close(jobID string, forget bool) []Row {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.jobs[jobID]
	if f == nil {
		return nil
	}
	if forget {
		delete(t.jobs, jobID)
	}
	if f.closed {
		return nil
	}
	f.closed = true
	f.open = -1
	for i := range f.calls {
		if f.calls[i].Status == "running" {
			f.calls[i].Status = string(events.ToolOutcomeUnknown)
			f.calls[i].Err = "No terminal tool event received"
			f.calls[i].Elapsed = time.Since(f.calls[i].Started)
		}
	}
	if len(f.rows) == 0 {
		return nil
	}
	// A provider can omit terminal hooks. Reconcile those calls as unknown at
	// job close so the row never renders an implied success.
	for i := range f.rows {
		if f.rows[i].Active > 0 {
			f.rows[i].Unknown += f.rows[i].Active
			f.rows[i].Count += f.rows[i].Active
			f.rows[i].ReconciledUnknown += f.rows[i].Active
			f.rows[i].Active = 0
		}
	}
	return append([]Row(nil), f.rows...)
}

func (t *Tracker) foldFor(jobID string) *fold {
	if t.jobs == nil {
		t.jobs = make(map[string]*fold)
	}
	f := t.jobs[jobID]
	if f == nil {
		f = &fold{open: -1, byCall: make(map[string]int), finished: make(map[string]bool), callIndex: make(map[string]int)}
		t.jobs[jobID] = f
	}
	return f
}
