package brain

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

const toolTimeAnswerInstruction = "The time budget for this job is nearly used. No more tools are available. Answer now using the results already collected. State what was completed, what remains, and any concrete blocker. Do not claim unfinished work succeeded, promise to answer later, or describe tool-loop reasoning."

const toolRepeatAnswerInstruction = "The same tool call was repeated several times without progress. No more tools are available. Answer now using the results already collected. State what was completed, what remains, and the concrete blocker that stopped progress. Do not claim unfinished work succeeded, promise to answer later, or describe tool-loop reasoning."

const maxRepeatedToolRounds = 3

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
	lastCalls   string
	repeats     int
}

func newToolRounds(cfg *config.Config, opts StreamOptions) *toolRounds {
	return &toolRounds{
		worker: (cfg != nil && cfg.IsWorker) || opts.workerBudget() != nil,
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
		r.instruction = r.limitInstruction(ctx)
	}
	return r.instruction != "", r.instruction
}

func (r *toolRounds) limitInstruction(ctx context.Context) string {
	if deadline, ok := WorkerDeadline(ctx); ok {
		if time.Until(deadline) <= workerTimeReserve(deadline.Sub(r.start)) {
			return toolTimeAnswerInstruction
		}
	}
	if left, ok := r.budget.RemainingRequests(); ok && left <= 1 {
		return toolLimitAnswerInstruction
	}
	return ""
}

// observe records the tool calls of a completed round for the no-progress guard.
func (r *toolRounds) observe(calls []api.ToolCall) {
	if !r.worker {
		return
	}
	sig := canonicalToolCalls(calls)
	if sig == r.lastCalls {
		r.repeats++
	} else {
		r.lastCalls, r.repeats = sig, 1
	}
	if r.repeats >= maxRepeatedToolRounds && r.instruction == "" {
		r.instruction = toolRepeatAnswerInstruction
	}
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
