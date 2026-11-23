package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

const fallbackWorkerContext = 131072

// keptAfterTask splits the messages after the task at index task: every user
// turn before cut and everything from cut on is kept, in order; the rest is
// dropped for the summary.
func keptAfterTask(history []api.Message, task, cut int) (kept, dropped []int) {
	for i := task + 1; i < len(history); i++ {
		if i >= cut || history[i].Role == "user" {
			kept = append(kept, i)
		} else {
			dropped = append(dropped, i)
		}
	}
	return kept, dropped
}

func userCount(history []api.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

func firstUserIndex(history []api.Message) int {
	return slices.IndexFunc(history, func(m api.Message) bool { return m.Role == "user" })
}

// Resolve once, using the selected worker's server and model. Never borrow the
// voice window or mutate the host's frozen admission configuration.
func resolveWorkerContext(ctx context.Context, client *api.Client, cfg *config.Config) (*config.Config, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	model, err := client.Show(ctx, &api.ShowRequest{Model: cfg.OllamaModel})
	if err != nil {
		return nil, fmt.Errorf("resolve worker model context: %w", err)
	}
	n := fallbackWorkerContext
	architecture, _ := model.ModelInfo["general.architecture"].(string)
	if raw, ok := model.ModelInfo[architecture+".context_length"]; ok {
		encoded, err := json.Marshal(raw)
		if err == nil {
			if advertised, err := strconv.Atoi(string(encoded)); err == nil && advertised > 0 {
				n = advertised
			}
		}
	}
	w := *cfg
	w.OllamaNumCtx, w.ContinuityNumCtxCeil = n, n
	return &w, nil
}

// Workers can spend many tool rounds on a single user request. Compact inside
// that loop, before inference, rather than waiting for a new foreground turn.
// Only the working projection changes: canonical tool evidence stays intact.
// The task and every later user turn (a correction, a repair request) are
// kept as written and in order; the exchanges between them are summarized.
func (o *OllamaBrain) prepareWorkerContext(ctx context.Context, skillCtx string, tools api.Tools, opts StreamOptions) error {
	if o.cfg == nil || !o.cfg.IsWorker || o.audioResolver != nil || o.cfg.ContinuitySoftLimitRatio <= 0 || o.effectiveNumCtx() <= 0 {
		return nil
	}
	limit := int(float64(o.effectiveNumCtx()) * min(o.cfg.ContinuitySoftLimitRatio, 0.9))
	beforeTokens := estimateMessageTokens(o.buildMessages(skillCtx), tools)
	if beforeTokens <= limit {
		return nil
	}
	// The last request is the answer's. A summary would spend it; the
	// request's own size check decides whether the answer still fits.
	if left, counted := opts.workerBudget().RemainingRequests(); counted && left == 1 {
		return nil
	}
	task := firstUserIndex(o.history)
	if task < 0 || !slices.ContainsFunc(o.history[task+1:], func(m api.Message) bool { return m.Role != "user" }) {
		return fmt.Errorf("%w: worker task and instructions leave insufficient response space; increase voice_worker_num_ctx or shorten the task", ErrPromptBudget)
	}
	before, working := o.history, o.working
	messages := o.buildMessages(skillCtx)
	messages = append(messages, api.Message{Role: "user", Content: systemPrompt(o.cfg, "worker-compact", nil)})
	// A large tool result can cross the threshold in one round. Bound only
	// this summary request's copies, leaving the original evidence untouched.
	output := min(4096, o.effectiveNumCtx()/4)
	messageLimit := min(o.effectiveNumCtx()*9/10, o.effectiveNumCtx()-output)
	if excess := estimateMessageTokens(messages, nil) - messageLimit; excess > 0 {
		shrinkToolResults(messages, max(0, toolContentBytes(messages)-excess*4))
	}
	if err := o.messagesBudgetError(messages, nil); err != nil {
		return fmt.Errorf("compact worker context: %w", err)
	}
	if estimateMessageTokens(messages, nil) > messageLimit {
		return fmt.Errorf("%w: worker compaction needs more output space; increase voice_worker_num_ctx", ErrPromptBudget)
	}
	req := o.newChatRequest(messages, nil)
	stream := false
	req.Stream = &stream
	req.Think = &api.ThinkValue{Value: false}
	if req.Options == nil {
		req.Options = map[string]any{}
	}
	req.Options["num_predict"] = output
	var summary, reason string
	complete := false
	// With two requests left the summary gets one: a resend would take the
	// answer's.
	summaryCtx, reserve := ctx, false
	if left, counted := opts.workerBudget().RemainingRequests(); counted && left <= 2 {
		summaryCtx, reserve = context.WithValue(ctx, disableOutageRetryKey{}, true), true
	}
	err := o.chat(summaryCtx, req, func(r api.ChatResponse) error {
		summary += r.Message.Content
		reason, complete = r.DoneReason, r.Done
		if r.Done {
			if opts.OnUsage != nil {
				opts.OnUsage(r.Metrics.PromptEvalCount, r.Metrics.EvalCount)
			}
			if opts.OnWorkerUsage != nil {
				u := WorkerUsage{}
				if r.Metrics.PromptEvalCount > 0 {
					n := r.Metrics.PromptEvalCount
					u.InputTokens = &n
				}
				if r.Metrics.EvalCount > 0 {
					n := r.Metrics.EvalCount
					u.OutputTokens = &n
				}
				opts.OnWorkerUsage(u)
			}
		}
		return nil
	})
	if err != nil {
		if reserve && ctx.Err() == nil && ollamaOutage(err) != nil || errors.Is(err, errRequestObsolete) {
			// The answer keeps its request; its own size check decides.
			return nil
		}
		return fmt.Errorf("compact worker context: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	summary = strings.TrimSpace(summary)
	if !complete || reason == "length" || summary == "" {
		return fmt.Errorf("compact worker context: summary incomplete")
	}
	// Prefer keeping the newest complete tool exchange verbatim, where it sits.
	// If it alone crowds the window, the summary carries its results instead.
	// Never retain a tool result without the assistant call that produced it.
	cuts := []int{len(before)}
	for i := len(before) - 1; i > task; i-- {
		if before[i].Role == "assistant" && len(before[i].ToolCalls) > 0 {
			if _, dropped := keptAfterTask(before, task, i); len(dropped) > 0 {
				cuts = []int{i, len(before)}
			}
			break
		}
	}
	notes := "Worker task notes: " + summary
	if n := userCount(before[task+1:]); n > 0 {
		instructions := "the first instruction"
		if n > 1 {
			instructions = fmt.Sprintf("the first %d instructions", n)
		}
		notes = fmt.Sprintf("Worker task notes, a summary of the work so far, written after %s below: %s", instructions, summary)
	}
	var kept, dropped []int
	for n, cut := range cuts {
		kept, dropped = keptAfterTask(before, task, cut)
		// Notes between the task and a correction are the worker's own record,
		// so the task is not folded into earlier context with them.
		role := ollamaSharedRole
		if slices.ContainsFunc(kept, func(k int) bool { return before[k].Role == "user" }) {
			role = "assistant"
		}
		o.history = append(append([]api.Message(nil), before[:task+1]...), api.Message{Role: role, Content: notes})
		for _, k := range kept {
			o.history = append(o.history, before[k])
		}
		if estimateMessageTokens(o.buildMessages(skillCtx), tools) <= limit {
			break
		}
		if n == len(cuts)-1 {
			o.history = before
			return fmt.Errorf("%w: worker summary leaves insufficient response space; increase voice_worker_num_ctx", ErrPromptBudget)
		}
	}
	var archived []api.Message
	var archivedTurns []Turn
	if len(working) == len(before) {
		o.working = append(append([]Turn(nil), working[:task+1]...), Turn{Role: "assistant", Content: notes})
		for _, k := range kept {
			o.working = append(o.working, working[k])
		}
		for _, d := range dropped {
			archivedTurns = append(archivedTurns, working[d])
		}
	}
	for _, d := range dropped {
		archived = append(archived, before[d])
	}
	o.archiveTurns(archived, archivedTurns)
	o.emitContinuity(events.Info{Message: fmt.Sprintf("continuity.worker_compact before=%d after=%d num_ctx=%d", beforeTokens, estimateMessageTokens(o.buildMessages(skillCtx), tools), o.effectiveNumCtx())})
	return nil
}
