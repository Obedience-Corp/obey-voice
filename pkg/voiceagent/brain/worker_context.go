package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

const fallbackWorkerContext = 131072

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
func (o *OllamaBrain) prepareWorkerContext(ctx context.Context, skillCtx string, tools api.Tools, opts StreamOptions) error {
	if o.cfg == nil || !o.cfg.IsWorker || o.audioResolver != nil || o.cfg.ContinuitySoftLimitRatio <= 0 || o.effectiveNumCtx() <= 0 {
		return nil
	}
	limit := int(float64(o.effectiveNumCtx()) * min(o.cfg.ContinuitySoftLimitRatio, 0.9))
	beforeTokens := estimateMessageTokens(o.buildMessages(skillCtx), tools)
	if beforeTokens <= limit {
		return nil
	}
	user := lastUserIndex(o.history)
	if user < 0 || user+1 >= len(o.history) {
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
	err := o.chat(ctx, req, func(r api.ChatResponse) error {
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
		return fmt.Errorf("compact worker context: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	summary = strings.TrimSpace(summary)
	if !complete || reason == "length" || summary == "" {
		return fmt.Errorf("compact worker context: summary incomplete")
	}
	// Prefer keeping the newest complete tool exchange verbatim. If it alone
	// crowds the window, the summary carries its results instead. Never retain
	// a tool result without the assistant call that produced it.
	cut := len(before)
	for i := len(before) - 1; i > user; i-- {
		if before[i].Role == "assistant" && len(before[i].ToolCalls) > 0 {
			cut = i
			break
		}
	}
	notes := "Worker task notes: " + summary
	for {
		o.history = append(append(append([]api.Message(nil), before[:user+1]...), api.Message{Role: ollamaSharedRole, Content: notes}), before[cut:]...)
		if estimateMessageTokens(o.buildMessages(skillCtx), tools) <= limit {
			break
		}
		if cut == len(before) {
			o.history = before
			return fmt.Errorf("%w: worker summary leaves insufficient response space; increase voice_worker_num_ctx", ErrPromptBudget)
		}
		cut = len(before)
	}
	if len(working) == len(before) {
		o.working = append(append(append([]Turn(nil), working[:user+1]...), Turn{Role: "assistant", Content: notes}), working[cut:]...)
	}
	o.archiveMessages(before[user+1 : cut])
	o.emitContinuity(events.Info{Message: fmt.Sprintf("continuity.worker_compact before=%d after=%d num_ctx=%d", beforeTokens, estimateMessageTokens(o.buildMessages(skillCtx), tools), o.effectiveNumCtx())})
	return nil
}
