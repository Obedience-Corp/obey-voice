package brain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const toolTimeAnswerInstruction = "The time budget for this job is nearly used. No more tools are available. Answer now using the results already collected. State what was completed, what remains, and any concrete blocker. Do not claim unfinished work succeeded, promise to answer later, or describe tool-loop reasoning."

const toolRepeatAnswerInstruction = "The same tool calls kept returning the same results without progress. No more tools are available. Answer now using the results already collected. State what was completed, what remains, and the concrete blocker that stopped progress. Do not claim unfinished work succeeded, promise to answer later, or describe tool-loop reasoning."

const (
	maxRepeatedToolRounds = 3
	repeatWindowRounds    = 12
)

const (
	stopRoundLimit = "tool round limit reached"
	stopTime       = "worker time budget nearly used"
	stopRepeat     = "worker repeated the same tool call"
)

// workerTimeReserve is how much of a deadline a worker keeps for its final
// answer, never more than half of it. It is a variable so tests need not wait
// out the real reserve.
var workerTimeReserve = func(total time.Duration) time.Duration {
	return min(max(total/10, 15*time.Second), time.Minute, total/2)
}

// toolRounds decides when a turn's tool loop must stop and answer. Live turns
// stop after maxToolIterations rounds. Workers run until the job is done,
// bounded by their deadline, request budget, and a no-progress guard.
type toolRounds struct {
	worker      bool
	start       time.Time
	budget      *WorkerBudget
	instruction string
	stop        string
	recent      []string
	stale       int
}

func newToolRounds(cfg *config.Config, opts StreamOptions) *toolRounds {
	return &toolRounds{
		worker: (cfg != nil && cfg.IsWorker) || opts.workerBudget() != nil,
		stop:   stopRoundLimit,
		start:  time.Now(),
		budget: opts.workerBudget(),
	}
}

// more reports whether round i may run. Workers are not bounded by count.
func (r *toolRounds) more(i int) bool {
	return r.worker || i <= maxToolIterations
}

// final reports whether round i is the tools-free answer round, and the
// instruction to append to it. Once a round is final, later rounds stay final.
func (r *toolRounds) final(ctx context.Context, i int) (bool, string) {
	if !r.worker {
		if i >= maxToolIterations {
			return true, toolLimitAnswerInstruction
		}
		return false, ""
	}
	if r.instruction == "" {
		r.instruction = r.limitInstruction(ctx, i)
	}
	return r.instruction != "", r.instruction
}

func (r *toolRounds) limitInstruction(ctx context.Context, i int) string {
	deadline, timed := WorkerDeadline(ctx)
	if timed && time.Until(deadline) <= workerTimeReserve(deadline.Sub(r.start)) {
		r.stop = stopTime
		return toolTimeAnswerInstruction
	}
	left, counted := r.budget.RemainingRequests()
	if counted && left <= 1 {
		return toolLimitAnswerInstruction
	}
	return ""
}

// stopErr describes a final round that produced no usable answer.
func (r *toolRounds) stopErr(detail string) error {
	return fmt.Errorf("%s: %s", r.stop, detail)
}

// observe records a completed round for the no-progress guard. results holds
// what each call returned, in call order. A worker must answer once a round
// has recurred with the same results and nothing new has happened in between.
// A recurring round separated by new work, such as a test rerun after each
// edit, is progress.
func (r *toolRounds) observe(calls []api.ToolCall, results []string) {
	if !r.worker {
		return
	}
	sig := roundSignature(calls, results)
	if slices.Contains(r.recent, sig) {
		r.stale++
	} else {
		r.stale = 0
	}
	r.recent = append(r.recent, sig)
	if len(r.recent) > repeatWindowRounds {
		r.recent = r.recent[1:]
	}
	stuck := r.stale >= maxRepeatedToolRounds-1 && count(r.recent, sig) >= maxRepeatedToolRounds
	if r.instruction == "" && stuck {
		r.instruction, r.stop = toolRepeatAnswerInstruction, stopRepeat
	}
}

func count(sigs []string, sig string) int {
	n := 0
	for _, s := range sigs {
		if s == sig {
			n++
		}
	}
	return n
}

// roundSignature hashes each call with its result so large results are not
// retained and call order does not matter.
func roundSignature(calls []api.ToolCall, results []string) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		var result string
		if i < len(results) {
			result = results[i]
		}
		sum := sha256.Sum256([]byte(result))
		parts[i] = canonicalToolCalls([]api.ToolCall{c}) + "\x02" + string(sum[:])
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return string(sum[:])
}

// canonicalToolCalls encodes names and arguments so that argument key order
// and call order do not matter.
func canonicalToolCalls(calls []api.ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		var args any
		if raw, err := json.Marshal(c.Function.Arguments); err == nil {
			_ = json.Unmarshal(raw, &args)
		}
		canon, _ := json.Marshal(args)
		parts = append(parts, c.Function.Name+"\x00"+string(canon))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x01")
}
