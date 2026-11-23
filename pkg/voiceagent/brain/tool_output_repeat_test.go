package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// listingCommands each print the same listing of a folder holding a.txt,
// b.txt and notes.md, worded differently so no two calls are alike.
var listingCommands = []string{
	"ls", "ls -1", "ls | cat", "ls -1 | cat", "ls .", "ls ./", "ls -- .", "command ls",
	"ls | sort", "ls | cat | cat", "ls -1 .", "ls | head -n 100", "ls -1 ./", "ls -1 -- .",
}

func commandCall(command string) api.ToolCall {
	return api.ToolCall{Function: api.ToolCallFunction{Name: "run_command", Arguments: mustArgs(map[string]any{"command": command})}}
}

func listingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runJobIn drives one ThinkFull turn in dir against a fake Ollama server.
func runJobIn(t *testing.T, ctx context.Context, dir string, worker bool, opts StreamOptions, reply func(n int, req api.ChatRequest) api.ChatResponse) roundsRun {
	t.Helper()
	var mu sync.Mutex
	var reqs []api.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		reqs = append(reqs, req)
		n := len(reqs)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(reply(n, req))
	}))
	defer server.Close()
	host, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: dir, cfg: &config.Config{MaxHistory: 100, OllamaNumCtx: 32768, IsWorker: worker}}
	var out roundsRun
	out.answer, out.err = o.ThinkFull(ctx, "Do the job.", opts)
	mu.Lock()
	out.reqs = append(out.reqs, reqs...)
	mu.Unlock()
	return out
}

type sameOutputRecord struct {
	reported []int
	forced   []ForcedAnswer
}

func recordingOptions() (StreamOptions, *sameOutputRecord) {
	rec := &sameOutputRecord{}
	opts := WorkerStreamOptions(true)
	opts.OnSameOutput = func(n int) { rec.reported = append(rec.reported, n) }
	opts.OnForcedAnswer = func(reason ForcedAnswer) { rec.forced = append(rec.forced, reason) }
	return opts, rec
}

func assertKeptWorking(t *testing.T, run roundsRun, rounds int, rec *sameOutputRecord) {
	t.Helper()
	if run.err != nil || run.answer == "" {
		t.Fatalf("answer=%q err=%v", run.answer, run.err)
	}
	if len(run.reqs) != rounds+1 || len(rec.forced) != 0 {
		t.Fatalf("the job was stopped: requests=%d forced=%v", len(run.reqs), rec.forced)
	}
	for i, req := range run.reqs {
		if len(req.Tools) == 0 || hasInstruction(req, toolRepeatAnswerInstruction) {
			t.Fatalf("request %d withheld tools or forced an answer", i+1)
		}
	}
}

func upTo(from, to int) []int {
	var out []int
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

// A worker once listed one folder over and over, rewording the command each
// time. Every round is new to the repeat guard, since the result echoes the
// command, but the listing never changed.
func TestRewordedListingsAreCountedAndTheJobKeepsRunning(t *testing.T) {
	dir := listingDir(t)
	opts, rec := recordingOptions()
	run := runJobIn(t, t.Context(), dir, true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		if n <= len(listingCommands) {
			return toolResponse([]api.ToolCall{commandCall(listingCommands[n-1])})
		}
		return answerResponse("The folder holds three files.")
	})
	assertKeptWorking(t, run, len(listingCommands), rec)
	if want := upTo(sameOutputShown, len(listingCommands)); !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
	if got := SameOutputNote(rec.reported[len(rec.reported)-1]); got != fmt.Sprintf("same output for %d rounds", len(listingCommands)) {
		t.Fatalf("note = %q", got)
	}
}

func TestRewordedListingsStillEndOnCancel(t *testing.T) {
	dir := listingDir(t)
	opts, rec := recordingOptions()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run := runJobIn(t, ctx, dir, true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		if n == 12 {
			cancel()
		}
		return toolResponse([]api.ToolCall{commandCall(listingCommands[(n-1)%len(listingCommands)])})
	})
	if !errors.Is(run.err, context.Canceled) {
		t.Fatalf("a cancel did not end the job: answer=%q err=%v", run.answer, run.err)
	}
	if want := upTo(sameOutputShown, 11); !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
}

// Writing several different files prints nothing each time. That counts as
// the same output; nothing is stopped, so the count is only a description.
func TestDistinctSilentWritesCountAndAreNotStopped(t *testing.T) {
	writes := []string{"printf a > one.txt", "printf b > two.txt", "printf c > three.txt", "printf d > four.txt", "printf e > five.txt", "printf f > six.txt"}
	opts, rec := recordingOptions()
	run := runJobIn(t, t.Context(), t.TempDir(), true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		if n <= len(writes) {
			return toolResponse([]api.ToolCall{commandCall(writes[n-1])})
		}
		return answerResponse("All six files are written.")
	})
	assertKeptWorking(t, run, len(writes), rec)
	if want := upTo(sameOutputShown, len(writes)); !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
}

// Polling a status that has not changed yet is the same output too.
func TestPollingAnUnchangedStatusCountsAndIsNotStopped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "status.txt"), []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	polls := []string{"cat status.txt", "cat ./status.txt", "cat -- status.txt", "cat status.txt | cat", "head -n 5 status.txt", "tail -n 5 status.txt", "sleep 0; cat status.txt"}
	opts, rec := recordingOptions()
	run := runJobIn(t, t.Context(), dir, true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		if n <= len(polls) {
			return toolResponse([]api.ToolCall{commandCall(polls[n-1])})
		}
		return answerResponse("The status is still pending.")
	})
	assertKeptWorking(t, run, len(polls), rec)
	if want := upTo(sameOutputShown, len(polls)); !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
}

// Rerunning a failing check after an edit that fixes it is new output: a
// shown count is cleared once, and a check whose result changes every time
// is never counted.
func TestRerunAfterEachEditResetsTheCount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("FAIL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checks := []string{"cat result.txt", "cat ./result.txt", "cat -- result.txt", "cat result.txt | cat"}
	edit := func(content string) api.ToolCall {
		return api.ToolCall{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": "result.txt", "content": content})}}
	}
	opts, rec := recordingOptions()
	const rounds = 8
	run := runJobIn(t, t.Context(), dir, true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		switch {
		case n <= len(checks):
			return toolResponse([]api.ToolCall{commandCall(checks[n-1])})
		case n <= rounds:
			return toolResponse([]api.ToolCall{edit(fmt.Sprintf("attempt %d\n", n)), commandCall("cat result.txt")})
		}
		return answerResponse("The check passes now.")
	})
	assertKeptWorking(t, run, rounds, rec)
	if want := []int{3, 4, 0}; !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
}

// A batch is the same only when every tool in it returned the same thing,
// whatever order the calls came in.
func TestMixedBatchResetsWhenOneOutputChanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixed.txt"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	batches := [][]api.ToolCall{
		{commandCall("cat fixed.txt"), commandCall("echo steady")},
		{commandCall("printf 'steady\\n'"), commandCall("cat ./fixed.txt")},
		{commandCall("cat -- fixed.txt"), commandCall("echo steady | cat")},
		{commandCall("cat fixed.txt | cat"), commandCall("echo changed")},
		{commandCall("cat fixed.txt"), commandCall("echo changed again")},
	}
	opts, rec := recordingOptions()
	run := runJobIn(t, t.Context(), dir, true, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
		if n <= len(batches) {
			return toolResponse(batches[n-1])
		}
		return answerResponse("Both checks are done.")
	})
	assertKeptWorking(t, run, len(batches), rec)
	if want := []int{3, 0}; !slices.Equal(rec.reported, want) {
		t.Fatalf("reported %v, want %v", rec.reported, want)
	}
}

// One call repeated word for word is still the repeat guard's to stop, at
// the same round as before, with its reason; the count only describes it.
func TestIdenticalRepeatStillTripsTheGuardWithItsReason(t *testing.T) {
	dir := listingDir(t)
	opts, rec := recordingOptions()
	run := runJobIn(t, t.Context(), dir, true, opts, func(_ int, req api.ChatRequest) api.ChatResponse {
		if len(req.Tools) == 0 {
			return answerResponse("The listing never changed, so the job is blocked.")
		}
		return toolResponse([]api.ToolCall{commandCall("ls")})
	})
	if run.err != nil || run.answer == "" {
		t.Fatalf("answer=%q err=%v", run.answer, run.err)
	}
	if len(run.reqs) != maxRepeatedToolRounds+1 || !hasInstruction(run.reqs[len(run.reqs)-1], toolRepeatAnswerInstruction) {
		t.Fatalf("requests=%d; the repeat guard did not answer at its usual round", len(run.reqs))
	}
	if !slices.Equal(rec.forced, []ForcedAnswer{ForcedByRepeat}) || !slices.Equal(rec.reported, []int{3}) {
		t.Fatalf("forced=%v reported=%v", rec.forced, rec.reported)
	}
}

func TestForegroundTurnNeverReportsSameOutput(t *testing.T) {
	dir := listingDir(t)
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "full"}[streaming], func(t *testing.T) {
			var reported []int
			opts := WorkerStreamOptions(true)
			opts.OnSameOutput = func(n int) { reported = append(reported, n) }
			reply := func(n int, req api.ChatRequest) api.ChatResponse {
				if len(req.Tools) == 0 {
					return answerResponse("Here is the listing.")
				}
				return toolResponse([]api.ToolCall{commandCall(listingCommands[(n-1)%len(listingCommands)])})
			}
			var err error
			if streaming {
				err = runRounds(t, t.Context(), true, false, opts, reply).err
			} else {
				err = runJobIn(t, t.Context(), dir, false, opts, reply).err
			}
			if err != nil || len(reported) != 0 {
				t.Fatalf("err=%v reported=%v", err, reported)
			}
		})
	}
}

func TestSameOutputCountsOnlyCompletedRounds(t *testing.T) {
	var reported []int
	s := newSameOutput(true, func(n int) { reported = append(reported, n) })
	calls := []api.ToolCall{commandCall("ls")}
	done, cancel := context.WithCancel(t.Context())
	cancel()
	for range 5 {
		s.observe(t.Context(), calls, nil)
		s.observe(done, calls, []string{"x"})
		s.observe(t.Context(), nil, nil)
	}
	if s.rounds != 0 || len(reported) != 0 {
		t.Fatalf("rounds=%d reported=%v", s.rounds, reported)
	}
	if newSameOutput(false, func(int) {}) != nil || newSameOutput(true, nil) != nil {
		t.Fatal("a live turn or a missing hook should not count")
	}
}

func TestToolOutputLeavesOutTheEchoedCommand(t *testing.T) {
	a, _ := json.Marshal(map[string]string{"output": "a.txt\n", "command": "ls"})
	b, _ := json.Marshal(map[string]string{"output": "a.txt\n", "command": "ls -1"})
	if toolOutput("run_command", string(a)) != "a.txt\n" || toolOutput("run_command", string(a)) != toolOutput("run_command", string(b)) {
		t.Fatal("reworded commands with the same output differ")
	}
	if got := toolOutput("run_command", "error: command is required"); got != "error: command is required" {
		t.Fatalf("an undecodable result should be used as it is: %q", got)
	}
	if got := toolOutput("read_file", string(a)); got != string(a) {
		t.Fatalf("other tools use their result text: %q", got)
	}
	if SameOutputNote(sameOutputShown-1) != "" || SameOutputNote(0) != "" {
		t.Fatal("a short run should have no note")
	}
}
