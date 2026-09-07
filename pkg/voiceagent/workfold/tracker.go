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
	Kind    string
	Count   int
	Failed  int
	Elapsed time.Duration
	// Last is the newest finished call's preview, or its error line when the
	// call failed without one.
	Last string
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
	if r.Count == 1 {
		noun = p.one
	}
	var b strings.Builder
	if p.verb != "" {
		b.WriteString(p.verb)
		b.WriteString(" ")
	}
	b.WriteString(strconv.Itoa(r.Count))
	b.WriteString(" ")
	b.WriteString(noun)
	if r.Failed > 0 {
		b.WriteString(" (")
		b.WriteString(strconv.Itoa(r.Failed))
		b.WriteString(" failed)")
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
	rows []Row
	// open is the index of the row still accepting calls, or -1.
	open int
	// byCall maps a call id to the row it opened, so a finish lands on the
	// right row even if a later kind has already opened another.
	byCall map[string]int
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
	var closed *Row
	if f.open >= 0 && f.rows[f.open].Kind == e.Kind {
		f.rows[f.open].Count++
	} else {
		if f.open >= 0 {
			row := f.rows[f.open]
			closed = &row
		}
		f.rows = append(f.rows, Row{Kind: e.Kind, Count: 1})
		f.open = len(f.rows) - 1
	}
	if e.ID != "" {
		f.byCall[e.ID] = f.open
	}
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
	if f == nil {
		return
	}
	idx, ok := f.byCall[e.ID]
	if !ok {
		if idx, ok = f.lastOfKind(e.Kind); !ok {
			return
		}
	}
	delete(f.byCall, e.ID)
	row := &f.rows[idx]
	if e.Err != "" {
		row.Failed++
	}
	row.Elapsed += e.Elapsed
	if e.Preview != "" {
		row.Last = e.Preview
	} else if e.Err != "" {
		row.Last = e.Err
	}
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
	rows := t.Rows(jobID)
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.String())
	}
	return strings.Join(parts, " · ")
}

// Close ends the job's open row, returns every row, and forgets the job.
func (t *Tracker) Close(jobID string) []Row {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.jobs[jobID]
	if f == nil {
		return nil
	}
	delete(t.jobs, jobID)
	f.open = -1
	if len(f.rows) == 0 {
		return nil
	}
	return append([]Row(nil), f.rows...)
}

func (t *Tracker) foldFor(jobID string) *fold {
	if t.jobs == nil {
		t.jobs = make(map[string]*fold)
	}
	f := t.jobs[jobID]
	if f == nil {
		f = &fold{open: -1, byCall: make(map[string]int)}
		t.jobs[jobID] = f
	}
	return f
}

// lastOfKind finds the newest row of a kind, for a finish whose call id was
// never seen (a provider that omits ids on one side).
func (f *fold) lastOfKind(kind string) (int, bool) {
	for i := len(f.rows) - 1; i >= 0; i-- {
		if f.rows[i].Kind == kind {
			return i, true
		}
	}
	return 0, false
}
