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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

type roundsRun struct {
	answer string
	err    error
	reqs   []api.ChatRequest
}

func readFileCall(path string) []api.ToolCall {
	return []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: mustArgs(map[string]any{"path": path})}}}
}

func answerResponse(text string) api.ChatResponse {
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", Content: text}}
}

func toolResponse(calls []api.ToolCall) api.ChatResponse {
	return api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Role: "assistant", ToolCalls: calls}}
}

// runRounds drives one turn against a fake Ollama server. reply receives the
// 1-based request number and the request.
func runRounds(t *testing.T, ctx context.Context, streaming bool, worker bool, opts StreamOptions, reply func(n int, req api.ChatRequest) api.ChatResponse) roundsRun {
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
	o := &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: t.TempDir(), cfg: &config.Config{MaxHistory: 100, OllamaNumCtx: 32768, IsWorker: worker}}
	var out roundsRun
	if streaming {
		stream, err := o.ThinkStream(ctx, "Do the job.", opts)
		if err != nil {
			t.Fatal(err)
		}
		out.answer, _ = drainStream(t, stream)
		out.err = (<-stream.Done).Err
	} else {
		out.answer, out.err = o.ThinkFull(ctx, "Do the job.", opts)
	}
	mu.Lock()
	out.reqs = append(out.reqs, reqs...)
	mu.Unlock()
	return out
}

func hasInstruction(req api.ChatRequest, instruction string) bool {
	for _, m := range req.Messages {
		if m.Content == instruction {
			return true
		}
	}
	return false
}

func eachMode(t *testing.T, f func(t *testing.T, streaming bool)) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "full"}[streaming], func(t *testing.T) { f(t, streaming) })
	}
}

func TestWorkerRunsPastRoundCap(t *testing.T) {
	const rounds = 25
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(n int, _ api.ChatRequest) api.ChatResponse {
			if n <= rounds {
				return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
			}
			return answerResponse("The job is finished and all files were checked.")
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if len(run.reqs) != rounds+1 {
			t.Fatalf("requests=%d", len(run.reqs))
		}
		for i, req := range run.reqs {
			if len(req.Tools) == 0 {
				t.Fatalf("request %d had tools withheld", i+1)
			}
			for _, instruction := range []string{toolLimitAnswerInstruction, toolTimeAnswerInstruction, toolRepeatAnswerInstruction} {
				if hasInstruction(req, instruction) {
					t.Fatalf("request %d carried a final-answer instruction", i+1)
				}
			}
		}
	})
}

func TestLiveTurnKeepsRoundCap(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, false, WorkerStreamOptions(true), func(n int, _ api.ChatRequest) api.ChatResponse {
			if n <= maxToolIterations {
				return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
			}
			return answerResponse("Here is what I found so far.")
		})
		if run.err != nil || len(run.reqs) != maxToolIterations+1 {
			t.Fatalf("requests=%d err=%v", len(run.reqs), run.err)
		}
		last := run.reqs[maxToolIterations]
		if len(last.Tools) != 0 || !hasInstruction(last, toolLimitAnswerInstruction) {
			t.Fatalf("last request is not the round-limit answer: %+v", last)
		}
	})
}

func TestWorkerDeadlineForcesFinalAnswer(t *testing.T) {
	var reserve atomic.Int64
	reserve.Store(int64(time.Minute))
	saved := workerTimeReserve
	workerTimeReserve = func(time.Duration) time.Duration { return time.Duration(reserve.Load()) }
	t.Cleanup(func() { workerTimeReserve = saved })

	eachMode(t, func(t *testing.T, streaming bool) {
		reserve.Store(int64(time.Minute))
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		run := runRounds(t, ctx, streaming, true, WorkerStreamOptions(true), func(n int, req api.ChatRequest) api.ChatResponse {
			if n == 4 {
				reserve.Store(int64(2 * time.Hour))
			}
			if len(req.Tools) == 0 {
				return answerResponse("Two files were read; the rest remain.")
			}
			return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if len(run.reqs) != 5 {
			t.Fatalf("requests=%d", len(run.reqs))
		}
		last := run.reqs[4]
		if len(last.Tools) != 0 || !hasInstruction(last, toolTimeAnswerInstruction) {
			t.Fatalf("last request is not the time answer: %+v", last)
		}
	})
}

func TestWorkerWithoutDeadlineHasNoTimeFinal(t *testing.T) {
	saved := workerTimeReserve
	workerTimeReserve = func(time.Duration) time.Duration { return 100 * time.Hour }
	t.Cleanup(func() { workerTimeReserve = saved })
	run := runRounds(t, t.Context(), false, true, WorkerStreamOptions(true), func(n int, _ api.ChatRequest) api.ChatResponse {
		if n <= 3 {
			return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
		}
		return answerResponse("Done reading all of the files.")
	})
	if run.err != nil || len(run.reqs) != 4 {
		t.Fatalf("requests=%d err=%v", len(run.reqs), run.err)
	}
	for _, req := range run.reqs {
		if len(req.Tools) == 0 {
			t.Fatal("tools withheld without a deadline")
		}
	}
}

func TestTieredWorkerLastRequestIsFinalAnswer(t *testing.T) {
	const limit = 5
	eachMode(t, func(t *testing.T, streaming bool) {
		opts := WorkerStreamOptions(true)
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: limit})
		run := runRounds(t, t.Context(), streaming, false, opts, func(n int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("Four files were read before the request budget ran out.")
			}
			return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if len(run.reqs) != limit {
			t.Fatalf("requests=%d", len(run.reqs))
		}
		if last := run.reqs[limit-1]; len(last.Tools) != 0 || !hasInstruction(last, toolLimitAnswerInstruction) {
			t.Fatalf("last request is not the final answer: %+v", last)
		}
	})
}

func TestWorkerToolCallBudgetStillFails(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		opts := WorkerStreamOptions(true)
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxToolCalls: 3})
		run := runRounds(t, t.Context(), streaming, false, opts, func(n int, _ api.ChatRequest) api.ChatResponse {
			return toolResponse(readFileCall(fmt.Sprintf("file-%d.txt", n)))
		})
		if !errors.Is(run.err, ErrWorkerBudgetExceeded) {
			t.Fatalf("err=%v", run.err)
		}
	})
}

func TestRepeatedToolCallForcesFinalAnswer(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(_ int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("The file could not be read, so the job is blocked.")
			}
			return toolResponse(readFileCall("same.txt"))
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if len(run.reqs) != maxRepeatedToolRounds+1 {
			t.Fatalf("requests=%d", len(run.reqs))
		}
		if last := run.reqs[len(run.reqs)-1]; len(last.Tools) != 0 || !hasInstruction(last, toolRepeatAnswerInstruction) {
			t.Fatalf("last request is not the no-progress answer: %+v", last)
		}
	})
}

func TestDistinctToolCallsDoNotTriggerRepeatGuard(t *testing.T) {
	pattern := []string{"a", "a", "b", "b", "c", "c", "d", "d"}
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(n int, _ api.ChatRequest) api.ChatResponse {
			if n <= len(pattern) {
				return toolResponse(readFileCall(pattern[n-1]))
			}
			return answerResponse("All reads are complete.")
		})
		if run.err != nil || len(run.reqs) != len(pattern)+1 {
			t.Fatalf("requests=%d err=%v", len(run.reqs), run.err)
		}
		for _, req := range run.reqs {
			if len(req.Tools) == 0 || hasInstruction(req, toolRepeatAnswerInstruction) {
				t.Fatal("repeat guard fired on distinct calls")
			}
		}
	})
}

func TestCanonicalToolCallsIgnoresArgumentOrder(t *testing.T) {
	var a, b api.ToolCallFunctionArguments
	if err := json.Unmarshal([]byte(`{"path":"x","limit":1}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"limit":1,"path":"x"}`), &b); err != nil {
		t.Fatal(err)
	}
	ca := []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: a}}}
	cb := []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_file", Arguments: b}}}
	if canonicalToolCalls(ca) != canonicalToolCalls(cb) {
		t.Fatal("argument order changed the signature")
	}
}

func TestHostedWorkerDeadlineForcesFinalAnswer(t *testing.T) {
	saved := workerTimeReserve
	workerTimeReserve = func(time.Duration) time.Duration { return time.Minute }
	t.Cleanup(func() { workerTimeReserve = saved })

	eachMode(t, func(t *testing.T, streaming bool) {
		ctx := WithWorkerDeadline(t.Context(), time.Now().Add(30*time.Second))
		run := runRounds(t, ctx, streaming, true, WorkerStreamOptions(true), func(_ int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("Nothing was read before time ran out.")
			}
			return toolResponse(readFileCall("file.txt"))
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if len(run.reqs) != 1 || len(run.reqs[0].Tools) != 0 || !hasInstruction(run.reqs[0], toolTimeAnswerInstruction) {
			t.Fatalf("requests=%d, first is not the time answer", len(run.reqs))
		}
	})
}

func TestWorkerDeadlinePrefersTheEarlier(t *testing.T) {
	near, far := time.Now().Add(time.Minute), time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(t.Context(), far)
	defer cancel()
	if got, ok := WorkerDeadline(WithWorkerDeadline(ctx, near)); !ok || !got.Equal(near) {
		t.Fatalf("hosted earlier: got %v ok=%v", got, ok)
	}
	ctx, cancel = context.WithDeadline(t.Context(), near)
	defer cancel()
	if got, ok := WorkerDeadline(WithWorkerDeadline(ctx, far)); !ok || !got.Equal(near) {
		t.Fatalf("context earlier: got %v ok=%v", got, ok)
	}
	if _, ok := WorkerDeadline(t.Context()); ok {
		t.Fatal("deadline reported without one")
	}
}

func TestWorkerTimeReserveLeavesShortBudgetsRoomToWork(t *testing.T) {
	for total, want := range map[time.Duration]time.Duration{
		10 * time.Second: 5 * time.Second,
		20 * time.Second: 10 * time.Second,
		2 * time.Minute:  15 * time.Second,
		5 * time.Minute:  30 * time.Second,
		10 * time.Minute: time.Minute,
		time.Hour:        time.Minute,
	} {
		if got := workerTimeReserve(total); got != want {
			t.Errorf("reserve(%v)=%v, want %v", total, got, want)
		}
	}
}

func TestWorkerWithNoLimitsIsNotStoppedByRoundCount(t *testing.T) {
	r := newToolRounds(&config.Config{IsWorker: true}, WorkerStreamOptions(true))
	for i := range 500 {
		if final, _ := r.final(t.Context(), i); final {
			t.Fatalf("round %d was final", i)
		}
		r.observe(readFileCall("poll"), []string{fmt.Sprintf("output %d", i)})
	}
}

func TestRoundSignatureIncludesResults(t *testing.T) {
	calls := readFileCall("x")
	if roundSignature(calls, []string{"one"}) == roundSignature(calls, []string{"two"}) {
		t.Fatal("different results shared a signature")
	}
	if roundSignature(calls, []string{"one"}) != roundSignature(readFileCall("x"), []string{"one"}) {
		t.Fatal("equal rounds differed")
	}
}

func TestChangingResultsDoNotTriggerRepeatGuard(t *testing.T) {
	const polls = 20
	eachMode(t, func(t *testing.T, streaming bool) {
		path := filepath.Join(t.TempDir(), "status.txt")
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(n int, _ api.ChatRequest) api.ChatResponse {
			if n <= polls {
				if err := os.WriteFile(path, []byte(fmt.Sprintf("progress %d", n)), 0o600); err != nil {
					t.Error(err)
				}
				return toolResponse(readFileCall(path))
			}
			return answerResponse("The job finished after polling its status.")
		})
		if run.err != nil || len(run.reqs) != polls+1 {
			t.Fatalf("requests=%d err=%v", len(run.reqs), run.err)
		}
		for _, req := range run.reqs {
			if len(req.Tools) == 0 || hasInstruction(req, toolRepeatAnswerInstruction) {
				t.Fatal("repeat guard fired while results changed")
			}
		}
	})
}

func TestAlternatingCallsWithSameResultsForceFinalAnswer(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(n int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("Two files keep failing to read, so the job is blocked.")
			}
			return toolResponse(readFileCall([]string{"a", "b"}[n%2]))
		})
		if run.err != nil || run.answer == "" {
			t.Fatalf("answer=%q err=%v", run.answer, run.err)
		}
		if want := 2*maxRepeatedToolRounds - 1 + 1; len(run.reqs) != want {
			t.Fatalf("requests=%d, want %d", len(run.reqs), want)
		}
		if last := run.reqs[len(run.reqs)-1]; len(last.Tools) != 0 || !hasInstruction(last, toolRepeatAnswerInstruction) {
			t.Fatalf("last request is not the no-progress answer: %+v", last)
		}
	})
}

func TestSingleRequestWorkerAnswersWithoutTools(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		opts := WorkerStreamOptions(true)
		opts.WorkerBudget = NewWorkerBudget(BudgetLimits{MaxRequests: 1})
		run := runRounds(t, t.Context(), streaming, false, opts, func(_ int, _ api.ChatRequest) api.ChatResponse {
			return answerResponse("Answered from what was given.")
		})
		if run.err != nil || len(run.reqs) != 1 || len(run.reqs[0].Tools) != 0 {
			t.Fatalf("requests=%d err=%v", len(run.reqs), run.err)
		}
	})
}

func TestFinalRoundFailureNamesItsCause(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		run := runRounds(t, t.Context(), streaming, true, WorkerStreamOptions(true), func(_ int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("")
			}
			return toolResponse(readFileCall("same.txt"))
		})
		if run.err == nil || !strings.Contains(run.err.Error(), stopRepeat) {
			t.Fatalf("err=%v", run.err)
		}
	})
}

func TestRecurringRoundBetweenNewWorkIsProgress(t *testing.T) {
	r := newToolRounds(&config.Config{IsWorker: true}, WorkerStreamOptions(true))
	for i := range 40 {
		r.observe(readFileCall(fmt.Sprintf("edit-%d.txt", i)), []string{"ok"})
		r.observe(readFileCall("test.log"), []string{"FAIL: same failure"})
		if final, _ := r.final(t.Context(), 2*i+2); final {
			t.Fatalf("stopped after %d attempts while new work was happening", i+1)
		}
	}
}
