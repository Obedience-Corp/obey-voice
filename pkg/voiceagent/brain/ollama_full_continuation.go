package brain

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// Shared across all tool rounds of one ThinkFull call. A model that keeps
// stopping at its own generation limit must not start an unbounded job.
const maxWorkerContinuations = 2

func fullWorkerContinuation(opts StreamOptions) bool {
	return opts.OmitTurnInstruction && !opts.TransientHistory && !opts.Background &&
		!opts.AgentContinuation && !opts.VoiceMode && opts.Discussion == nil &&
		opts.effectiveRouterPolicy() != RouterToolsRouteOnly
}

// Resume the current generation, keeping completed tool exchanges in the
// original request. Only the final complete message reaches the tool executor
// and canonical history. Private continuation instructions are never saved as
// human turns. Each follow-on request uses chat's existing job accounting.
// A failed request still returns the text of the segments before it.
func (o *OllamaBrain) chatFullWithContinuation(ctx context.Context, req *api.ChatRequest, opts StreamOptions, limit *responseBudget, attempts *int, chat func(context.Context, *api.ChatRequest, api.ChatResponseFunc) error) (api.Message, bool, error) {
	base := req.Messages
	var partial strings.Builder
	continued := false
	for {
		if err := ctx.Err(); err != nil {
			return api.Message{Content: partial.String()}, false, err
		}
		var response api.Message
		var reason string
		complete := false
		err := chat(ctx, req, func(resp api.ChatResponse) error {
			response, reason, complete = resp.Message, resp.DoneReason, resp.Done
			if opts.OnUsage != nil {
				opts.OnUsage(resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount)
			}
			if opts.OnWorkerUsage != nil {
				u := WorkerUsage{}
				if resp.Metrics.PromptEvalCount > 0 {
					in := resp.Metrics.PromptEvalCount
					u.InputTokens = &in
				}
				if resp.Metrics.EvalCount > 0 {
					out := resp.Metrics.EvalCount
					u.OutputTokens = &out
				}
				opts.OnWorkerUsage(u)
			}
			return limit.observe(resp)
		})
		if err != nil {
			return api.Message{Content: partial.String()}, false, err
		}
		if err := ctx.Err(); err != nil {
			return api.Message{Content: partial.String()}, false, err
		}
		if reason != "length" {
			if continued {
				if !complete {
					return api.Message{}, false, fmt.Errorf("ollama worker continuation ended without a completion signal; task incomplete")
				}
				combined := partial.String() + response.Content
				if len(response.ToolCalls) == 0 && (strings.TrimSpace(cleanForVoice(response.Content)) == "" || !hasSpeakableContent(cleanForVoice(combined))) {
					return api.Message{}, false, fmt.Errorf("ollama worker continuation returned no final answer; task incomplete")
				}
				response.Content = combined
			}
			return response, complete, nil
		}
		if !fullWorkerContinuation(opts) || !complete {
			return api.Message{}, false, fmt.Errorf("ollama full response truncated; generation limit reached")
		}
		if *attempts >= maxWorkerContinuations {
			return api.Message{}, false, fmt.Errorf("ollama worker continuation attempts exhausted after %d attempts; task incomplete", *attempts)
		}
		// Never execute or replay an invocation from a truncated response. Even
		// a typed call may have been followed by another, unfinished call.
		var thoughts textclean.ThinkFilter
		content, _ := thoughts.FeedSplit(partial.String() + response.Content)
		tail, _ := thoughts.FlushSplit()
		content, markupErr := textclean.StripToolMarkup(content + tail)
		prompt := "worker-continuation"
		if len(response.ToolCalls) > 0 || markupErr != nil {
			partial.Reset()
			prompt = "worker-truncated-tools"
		} else {
			partial.Reset()
			partial.WriteString(content)
		}
		messages := append([]api.Message(nil), base...)
		if partial.Len() > 0 {
			messages = append(messages, api.Message{Role: "assistant", Content: partial.String()})
		}
		messages = append(messages, api.Message{Role: "user", Content: systemPrompt(o.cfg, prompt, nil)})
		if err := o.messagesBudgetError(messages, req.Tools); err != nil {
			return api.Message{}, false, fmt.Errorf("continue truncated worker response: %w", err)
		}
		// Continuation is another generation, not a transport retry. Request,
		// output, runtime and admission policy stay on the same worker budget.
		next := *req
		next.Messages = messages
		req = &next
		*attempts += 1
		continued = true
		log.Printf("obey-voice: worker continuation attempt=%d max=%d done_reason=length", *attempts, maxWorkerContinuations)
		o.emitContinuity(events.Info{Message: fmt.Sprintf("Worker response reached its generation limit; continuing task (attempt %d/%d).", *attempts, maxWorkerContinuations)})
	}
}
