package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func readRecords(t *testing.T, path string) []record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func TestWriterRecordsConversationFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	bus.Emit(events.UserInput{Text: "hello there"})
	bus.Emit(events.ThinkingStarted{})
	bus.Emit(events.ResponseDelta{Text: "hi "})
	bus.Emit(events.ToolCallStarted{Name: "run_command", Summary: "ls"})
	bus.Emit(events.ToolCallFinished{Name: "run_command", Err: "exit 1"})
	bus.Emit(events.Error{Stage: "brain", Message: "ollama stream: boom"})
	bus.Emit(events.ResponseReady{Response: "hi, recovered", Degraded: true})
	bus.Emit(events.SpeakingStarted{Text: "hi recovered speech"})
	bus.Emit(events.SpeakingComplete{Elapsed: 1200 * time.Millisecond})
	bus.Emit(events.SessionWarning{PromptTokens: 9000, Threshold: 8000})
	bus.Emit(events.ConversationCompacted{TurnsBefore: 12, Summary: "prior turns summarized"})
	bus.Emit(events.TurnMetrics{Outcome: "completed", Degraded: true, ModelCompleteElapsed: 19 * time.Second})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	if len(recs) != 13 {
		t.Fatalf("got %d records, want 13:\n%+v", len(recs), recs)
	}
	if recs[0].Type != "header" || recs[0].V != Version {
		t.Fatalf("first record = %+v, want header v%d", recs[0], Version)
	}
	// File must not be world-readable (conversation content).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("transcript mode = %o, want 0600-class (no group/other)", info.Mode().Perm())
	}

	types := make([]string, 0, len(recs))
	lastSeq := int64(0)
	for _, r := range recs {
		types = append(types, r.Type)
		if r.Seq <= lastSeq {
			t.Fatalf("seq not monotonic: %+v", recs)
		}
		lastSeq = r.Seq
		if _, err := time.Parse(time.RFC3339Nano, r.TS); err != nil {
			t.Fatalf("bad ts on %+v: %v", r, err)
		}
	}
	want := []string{
		"header", "user", "thinking", "agent_delta", "tool_call", "tool_result",
		"error", "agent", "speaking", "speaking", "session_warning", "compacted", "metrics",
	}
	for i, ty := range want {
		if types[i] != ty {
			t.Fatalf("record %d type = %q, want %q (all: %v)", i, types[i], ty, types)
		}
	}

	if r := recs[5]; !r.IsError || r.Message != "exit 1" {
		t.Fatalf("tool_result = %+v, want is_error + message", r)
	}
	if r := recs[6]; r.Stage != "brain" || r.Message != "ollama stream: boom" {
		t.Fatalf("error record = %+v", r)
	}
	if r := recs[7]; !r.Degraded || r.Text != "hi, recovered" {
		t.Fatalf("agent record = %+v", r)
	}
	if r := recs[8]; r.State != "started" || r.Text != "hi recovered speech" {
		t.Fatalf("speaking started = %+v", r)
	}
	if r := recs[10]; r.Type != "session_warning" || r.Prefill != 9000 || r.Threshold != 8000 {
		t.Fatalf("session_warning = %+v", r)
	}
	if r := recs[11]; r.TurnsBefore != 12 || r.Summary != "prior turns summarized" {
		t.Fatalf("compacted = %+v", r)
	}
	if r := recs[12]; r.Outcome != "completed" || !r.Degraded || r.ModelS != 19.0 {
		t.Fatalf("metrics record = %+v", r)
	}
}

func TestWriterNeverBlocksAndCountsDrops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Writer with a full queue and no drain goroutine yet: enqueue must
	// return immediately and count the drop.
	w := &Writer{f: f, ch: make(chan record, 1), done: make(chan struct{})}
	w.ch <- record{Type: "user", Text: "kept", TS: time.Now().UTC().Format(time.RFC3339Nano), Seq: 1}
	w.seq.Store(1)

	done := make(chan struct{})
	go func() {
		// Non-durable into a full queue that already holds durable → drop the new non-durable.
		w.enqueue(record{Type: "agent_delta", Text: "noise"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}
	if w.dropped.Load() != 1 {
		t.Fatalf("dropped = %d, want 1", w.dropped.Load())
	}

	go w.run()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want kept + dropped: %+v", len(recs), recs)
	}
	if recs[0].Text != "kept" {
		t.Fatalf("first record = %+v", recs[0])
	}
	if recs[1].Type != "dropped" || recs[1].Dropped != 1 {
		t.Fatalf("drop record = %+v", recs[1])
	}
}

func TestWriterDurableSurvivesDeltaFlood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Capacity 4, no drain: flood with deltas then enqueue durable error+agent.
	w := &Writer{f: f, ch: make(chan record, 4), done: make(chan struct{})}
	for i := 0; i < 8; i++ {
		w.enqueue(record{Type: "agent_delta", Text: "x"})
	}
	w.enqueue(record{Type: "error", Stage: "brain", Message: "boom"})
	w.enqueue(record{Type: "agent", Text: "recovered", Degraded: true})

	go w.run()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	recs := readRecords(t, path)
	var sawError, sawAgent bool
	for _, r := range recs {
		if r.Type == "error" && r.Message == "boom" {
			sawError = true
		}
		if r.Type == "agent" && r.Text == "recovered" {
			sawAgent = true
		}
	}
	if !sawError || !sawAgent {
		t.Fatalf("durable records missing under delta flood: %+v", recs)
	}
}

func TestWriterIgnoresEventsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The bus outlives the conversation; late events must be dropped, not
	// panic on a closed channel or reopen the file.
	bus.Emit(events.UserInput{Text: "late"})
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	recs := readRecords(t, path)
	if len(recs) != 1 || recs[0].Type != "header" {
		t.Fatalf("got %+v, want header only", recs)
	}
}

func TestWriterConcurrentCloseAndEmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				bus.Emit(events.ResponseDelta{Text: "x"})
				bus.Emit(events.UserInput{Text: "u"})
			}
		}(i)
	}
	// Close while emitters race — must not panic.
	time.Sleep(2 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	// Late emits after close return without panic.
	bus.Emit(events.Error{Stage: "brain", Message: "late"})
}

func TestNewWriterFailsOnUnwritablePath(t *testing.T) {
	if _, err := NewWriter(filepath.Join(t.TempDir(), "missing", "t.jsonl")); err == nil {
		t.Fatal("expected error for unwritable path — a harness must not run blind")
	}
}

func TestWriterDoesNotRecordThinkingAsAgentDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	bus.Emit(events.ThinkingStarted{})
	bus.Emit(events.ThinkingDelta{Text: "the user said hi"})
	bus.Emit(events.ResponseDelta{Text: "Hello there."})
	bus.Emit(events.ResponseReady{Response: "Hello there."})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	var deltas []string
	for _, r := range recs {
		if strings.Contains(r.Text, "the user said hi") {
			t.Fatalf("thinking body recorded as %s: %+v", r.Type, r)
		}
		if r.Type == "agent_delta" {
			deltas = append(deltas, r.Text)
		}
	}
	if len(deltas) != 1 || deltas[0] != "Hello there." {
		t.Fatalf("agent_delta = %v, want speakable only", deltas)
	}
}

func TestFitTranscriptQueueDropsNonDurableFirst(t *testing.T) {
	q := []record{
		{Type: "user", Text: "u"},
		{Type: "agent_delta", Text: "d1"},
		{Type: "agent_delta", Text: "d2"},
		{Type: "error", Message: "e"},
	}
	got := fitTranscriptQueue(q, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Type != "user" || got[1].Type != "error" {
		t.Fatalf("expected user+error kept, got %+v", got)
	}
}

func readRawRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestWriterRecordsWorkerAttributionAndJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	bus := events.NewBus()
	w.Attach(bus)

	bus.Emit(events.JobStarted{ID: "job-1", Task: "read the readme"})
	bus.Emit(events.ToolCallStarted{ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read", Summary: "README.md"})
	bus.Emit(events.ToolCallFinished{
		ID: "call_1", JobID: "job-1", Name: "read_file", Kind: "read",
		Preview: "# obey", Elapsed: 1500 * time.Millisecond,
	})
	bus.Emit(events.JobFailed{ID: "job-1", Task: "read the readme", Err: "boom", Elapsed: 2 * time.Second})
	bus.Emit(events.JobFinished{ID: "job-2", Task: "count files", Elapsed: 3 * time.Second})
	bus.Emit(events.JobAborted{ID: "job-3", Task: "grep everything", Elapsed: 4 * time.Second})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recs := readRecords(t, path)
	if len(recs) != 7 {
		t.Fatalf("got %d records, want 7: %+v", len(recs), recs)
	}
	if r := recs[2]; r.Type != "tool_call" || r.ID != "call_1" || r.Kind != "read" || r.JobID != "job-1" {
		t.Fatalf("tool_call = %+v, want the id, kind and job attribution", r)
	}
	if r := recs[3]; r.Type != "tool_result" || r.JobID != "job-1" || r.ElapsedS != 1.5 {
		t.Fatalf("tool_result = %+v, want job attribution and elapsed", r)
	}

	jobs := map[string]record{}
	for _, r := range recs {
		if r.Type == "job" {
			jobs[r.Name] = r
		}
	}
	for _, tt := range []struct {
		id      string
		state   string
		elapsed float64
	}{
		{"job-1", "failed", 2},
		{"job-2", "finished", 3},
		{"job-3", "aborted", 4},
	} {
		r, ok := jobs[tt.id]
		if !ok {
			t.Fatalf("no job record for %s: %+v", tt.id, recs)
		}
		if r.State != tt.state || r.ElapsedS != tt.elapsed {
			t.Errorf("job %s = state %q elapsed %v, want %q / %v", tt.id, r.State, r.ElapsedS, tt.state, tt.elapsed)
		}
	}
	if got := jobs["job-1"].Message; got != "boom" {
		t.Errorf("failed job message = %q, want the error", got)
	}
	if got := jobs["job-1"].Text; got != "read the readme" {
		t.Errorf("job text = %q, want the task", got)
	}

	raw := readRawRecords(t, path)
	for _, key := range []string{"id", "kind", "job_id"} {
		if _, ok := raw[2][key]; !ok {
			t.Errorf("tool_call line missing %q key: %v", key, raw[2])
		}
	}
	if _, ok := raw[3]["elapsed_s"]; !ok {
		t.Errorf("tool_result line missing elapsed_s: %v", raw[3])
	}
}
