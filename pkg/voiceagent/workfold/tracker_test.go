package workfold

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func start(jobID, id, kind string) events.ToolCallStarted {
	return events.ToolCallStarted{ID: id, JobID: jobID, Name: kind, Kind: kind}
}

func finish(jobID, id, kind, errText string, elapsed time.Duration) events.ToolCallFinished {
	e := events.ToolCallFinished{ID: id, JobID: jobID, Name: kind, Kind: kind, Err: errText, Elapsed: elapsed}
	if errText == "" {
		e.Outcome = &events.ToolOutcome{Status: events.ToolOutcomeSucceeded, Elapsed: elapsed}
	}
	return e
}

func TestLiveBrainCallsAreIgnored(t *testing.T) {
	tr := New()

	if closed := tr.Add(events.ToolCallStarted{ID: "c1", Kind: "read"}); closed != nil {
		t.Fatalf("Add returned %+v for a call with no job id, want nil", *closed)
	}
	tr.Finish(events.ToolCallFinished{ID: "c1", Kind: "read"})

	if rows := tr.Rows(""); rows != nil {
		t.Fatalf("Rows(\"\") = %+v, want nothing: the live brain has no job", rows)
	}
	if got := tr.Summary(""); got != "" {
		t.Fatalf("Summary(\"\") = %q, want empty", got)
	}
}

func TestFinishWithoutAddIsDropped(t *testing.T) {
	tr := New()
	tr.Finish(finish("job-1", "c1", "read", "", time.Second))
	if rows := tr.Rows("job-1"); rows != nil {
		t.Fatalf("Rows = %+v, want nothing for a finish with no start", rows)
	}
}

func TestFoldingRuns(t *testing.T) {
	cases := []struct {
		name  string
		kinds []string
		want  []Row
	}{
		{
			name:  "consecutive same kind folds into one row",
			kinds: []string{"read", "read", "read"},
			want:  []Row{{Kind: "read", Count: 3}},
		},
		{
			name:  "a kind change opens a new row",
			kinds: []string{"read", "read", "execute"},
			want:  []Row{{Kind: "read", Count: 2}, {Kind: "execute", Count: 1}},
		},
		{
			name:  "returning to a kind opens a third row",
			kinds: []string{"read", "execute", "read"},
			want:  []Row{{Kind: "read", Count: 1}, {Kind: "execute", Count: 1}, {Kind: "read", Count: 1}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := New()
			for i, kind := range tc.kinds {
				tr.Add(start("job-1", "c"+strconv.Itoa(i), kind))
			}
			got := tr.Rows("job-1")
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i].Kind != tc.want[i].Kind || got[i].Started != tc.want[i].Count {
					t.Errorf("row %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestKindChangeReturnsTheClosedRow(t *testing.T) {
	tr := New()
	if closed := tr.Add(start("job-1", "c1", "read")); closed != nil {
		t.Fatalf("first Add closed %+v, want nil", *closed)
	}
	if closed := tr.Add(start("job-1", "c2", "read")); closed != nil {
		t.Fatalf("folding Add closed %+v, want nil", *closed)
	}
	tr.Finish(finish("job-1", "c1", "read", "", 10*time.Millisecond))
	tr.Finish(finish("job-1", "c2", "read", "", 10*time.Millisecond))

	closed := tr.Add(start("job-1", "c3", "execute"))
	if closed == nil {
		t.Fatal("a kind change closed no row, want the read row back for printing")
	}
	if closed.Kind != "read" || closed.Count != 2 || closed.Elapsed != 20*time.Millisecond {
		t.Fatalf("closed row = %+v, want read/2/20ms", *closed)
	}
	if closed.String() != "read 2 files (2 succeeded)" {
		t.Errorf("closed.String() = %q, want %q", closed.String(), "read 2 files (2 succeeded)")
	}
}

func TestFinishRecordsFailuresElapsedAndPreview(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "c1", "execute"))
	tr.Add(start("job-1", "c2", "execute"))
	tr.Finish(finish("job-1", "c1", "execute", "error: boom", 200*time.Millisecond))
	tr.Finish(events.ToolCallFinished{
		ID: "c2", JobID: "job-1", Kind: "execute", Preview: "ok", Elapsed: 300 * time.Millisecond,
	})

	rows := tr.Rows("job-1")
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one execute row", rows)
	}
	if rows[0].Failed != 1 {
		t.Errorf("Failed = %d, want 1", rows[0].Failed)
	}
	if rows[0].Elapsed != 500*time.Millisecond {
		t.Errorf("Elapsed = %v, want 500ms", rows[0].Elapsed)
	}
	if rows[0].Last != "ok" {
		t.Errorf("Last = %q, want the newest preview", rows[0].Last)
	}
}

func TestSummaryPhrasing(t *testing.T) {
	cases := []struct {
		name string
		rows []Row
		want string
	}{
		{name: "no rows", want: ""},
		{name: "one read", rows: []Row{{Kind: "read", Count: 1}}, want: "read 1 file"},
		{name: "many reads", rows: []Row{{Kind: "read", Count: 11}}, want: "read 11 files"},
		{name: "list_dir", rows: []Row{{Kind: "list_dir", Count: 2}}, want: "listed 2 folders"},
		{name: "write", rows: []Row{{Kind: "write", Count: 1}}, want: "wrote 1 file"},
		{name: "web_search", rows: []Row{{Kind: "web_search", Count: 3}}, want: "searched 3 times"},
		{name: "web_fetch", rows: []Row{{Kind: "web_fetch", Count: 1}}, want: "fetched 1 page"},
		{name: "skill", rows: []Row{{Kind: "skill", Count: 2}}, want: "loaded 2 skills"},
		{name: "task", rows: []Row{{Kind: "task", Count: 1}}, want: "delegated 1 task"},
		{name: "route falls back", rows: []Row{{Kind: "route", Count: 1}}, want: "1 tool call"},
		{name: "unknown kind falls back", rows: []Row{{Kind: "lsp", Count: 4}}, want: "4 tool calls"},
		{
			name: "terminal outcomes are explicit",
			rows: []Row{{Kind: "execute", Count: 2, Started: 2, Succeeded: 1, Failed: 1}},
			want: "ran 2 commands (1 succeeded, 1 failed)",
		},
		{
			name: "rows join in order",
			rows: []Row{{Kind: "read", Count: 11, Started: 11, Succeeded: 11}, {Kind: "execute", Count: 2, Started: 2, Failed: 1, Unknown: 1}},
			want: "read 11 files (11 succeeded) · ran 2 commands (1 failed, 1 unknown)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RowsSummary(tc.rows); got != tc.want {
				t.Fatalf("Summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJobsAreIsolated(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "a1", "read"))
	tr.Add(start("job-2", "b1", "execute"))
	tr.Add(start("job-1", "a2", "read"))

	if got := tr.Summary("job-1"); got != "2 files running" {
		t.Errorf("job-1 summary = %q, want 2 files running", got)
	}
	if got := tr.Summary("job-2"); got != "1 command running" {
		t.Errorf("job-2 summary = %q, want ran 1 command", got)
	}
}

func TestCloseReturnsRowsAndForgetsTheJob(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "c1", "read"))
	tr.Add(start("job-1", "c2", "execute"))

	rows := tr.Close("job-1")
	if len(rows) != 2 || rows[1].Kind != "execute" {
		t.Fatalf("Close = %+v, want both rows with execute last", rows)
	}
	if got := tr.Summary("job-1"); got != "" {
		t.Errorf("Summary after Close = %q, want empty: the job is forgotten", got)
	}
	if again := tr.Close("job-1"); again != nil {
		t.Errorf("second Close = %+v, want nil", again)
	}
}

func TestConcurrentJobsDoNotRace(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	for _, jobID := range []string{"job-1", "job-2"} {
		wg.Add(1)
		go func(jobID string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := jobID + "-" + strconv.Itoa(i)
				tr.Add(start(jobID, id, "read"))
				tr.Finish(finish(jobID, id, "read", "", time.Millisecond))
				tr.Summary(jobID)
			}
		}(jobID)
	}
	wg.Wait()

	for _, jobID := range []string{"job-1", "job-2"} {
		rows := tr.Rows(jobID)
		if len(rows) != 1 || rows[0].Count != 200 {
			t.Fatalf("%s rows = %+v, want one read row of 200", jobID, rows)
		}
	}
}

func TestTerminalAccountingDeduplicatesAndReconcilesUnknown(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "same", "execute"))
	tr.Add(start("job-1", "same", "execute")) // duplicate start
	tr.Finish(events.ToolCallFinished{JobID: "job-1", ID: "same", Kind: "execute", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}})
	tr.Finish(events.ToolCallFinished{JobID: "job-1", ID: "same", Kind: "execute", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded}})
	tr.Add(start("job-1", "left-open", "execute"))
	rows := tr.Close("job-1")
	if len(rows) != 1 || rows[0].Count != 2 || rows[0].Started != 2 || rows[0].Succeeded != 1 || rows[0].Unknown != 1 {
		t.Fatalf("rows = %+v, want two calls, one success and one reconciled unknown", rows)
	}
	if got := rows[0].Active; got != 0 {
		t.Errorf("active = %d after close, want 0", got)
	}
}

func TestUnmatchedCallIDDoesNotCloseAnotherCall(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "known", "execute"))
	tr.Finish(events.ToolCallFinished{JobID: "job-1", ID: "wrong", Kind: "execute", Err: "wrong call"})
	rows := tr.Close("job-1")
	if len(rows) != 1 || rows[0].Failed != 0 || rows[0].Unknown != 1 {
		t.Fatalf("rows = %+v, want unmatched finish left unknown", rows)
	}
}

func TestSummaryDistinguishesActiveAndUnknown(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "active", "execute"))
	if got := tr.Summary("job-1"); got != "1 command running" {
		t.Fatalf("start-only summary = %q, want active wording", got)
	}
	tr.Add(start("job-1", "done", "execute"))
	tr.Finish(finish("job-1", "done", "execute", "", 0))
	if got := tr.Summary("job-1"); got != "ran 1 command (1 succeeded, 1 active)" {
		t.Fatalf("partial summary = %q, want success plus active", got)
	}
	rows := tr.Close("job-1")
	if got := RowsSummary(rows); got != "ran 2 commands (1 succeeded, 1 unknown)" {
		t.Fatalf("close summary = %q, want unknown reconciliation", got)
	}
}

func TestSameRawCallIDIsScopedByJob(t *testing.T) {
	tr := New()
	tr.Add(start("job-1", "call", "read"))
	tr.Add(start("job-2", "call", "read"))
	tr.Finish(finish("job-1", "call", "read", "", 0))
	tr.Finish(events.ToolCallFinished{JobID: "job-2", ID: "call", Kind: "read", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeFailed}})
	if got := tr.Rows("job-1")[0].Succeeded; got != 1 {
		t.Errorf("job-1 succeeded = %d, want 1", got)
	}
	if got := tr.Rows("job-2")[0].Failed; got != 1 {
		t.Errorf("job-2 failed = %d, want 1", got)
	}
}

func TestFoldUsesTypedFactsWhenLegacyFieldsConflict(t *testing.T) {
	tr := New()
	tr.Add(start("j", "c", "execute"))
	tr.Finish(events.ToolCallFinished{JobID: "j", ID: "c", Kind: "execute", Err: "old error", Preview: "old preview", Elapsed: time.Second, Outcome: &events.ToolOutcome{Status: events.ToolOutcomeSucceeded, OutputPreview: "observed", Elapsed: 25 * time.Millisecond}})
	rows := tr.Rows("j")
	if len(rows) != 1 || rows[0].Last != "observed" || rows[0].Elapsed != 25*time.Millisecond || rows[0].Succeeded != 1 || rows[0].Failed != 0 {
		t.Fatalf("conflicting legacy fields changed folded facts: %+v", rows)
	}
}
