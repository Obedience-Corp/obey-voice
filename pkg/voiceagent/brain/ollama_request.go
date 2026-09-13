package brain

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ollama/ollama/api"
)

// newChatRequest builds a streaming chat request carrying the session's
// context policy: an explicit num_ctx so the server never silently truncates
// at its own default (top truncation eats the system prompt — and the
// persona — first), keep_alive so the model stays resident between voice
// turns (a reload is a multi-second mid-conversation stall), num_batch so
// prompt reading is not throttled by a window-derived guess, and think so
// qwen3-family models put CoT in Message.Thinking instead of spoken Content.
func (o *OllamaBrain) newChatRequest(messages []api.Message, tools api.Tools) *api.ChatRequest {
	stream := true
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  messages,
		Tools:     tools,
		Stream:    &stream,
		KeepAlive: o.keepAlive,
		Think:     o.resolvedThink(),
		Options:   o.requestOptions(),
	}
	return req
}

// requestOptions carries the two settings that decide how the prompt is read.
// They travel together because sending num_ctx alone is what produced the
// symptom this exists to fix: Ollama derives the prompt micro-batch mainly from
// the context window — 512 at or below 4096, 1024 above, 2048 above 32768,
// downgraded toward 512 when the model's predicted VRAM will not fit
// (server/sched.go automaticGenerationBatch at v0.32.14) — so raising the
// window to hold a conversation silently changed how the prompt is read. It
// weighs the model's memory footprint; it never weighs its architecture, which
// is the part that matters below.
//
// num_batch buys prefix REUSE, not prompt-reading speed. llama.cpp keeps its
// last reusable context checkpoint roughly one micro-batch back from the end of
// the prompt, so the server re-reads about num_batch tokens and restores the
// rest. Measured on qwen3.8-uncensored:27b at a ~1.2k-token conversational
// prompt, cached share tracks it almost exactly: 14 % skipped at batch 1024,
// 89 % at 128, 97 % at 32 — while raw prompt-reading throughput *falls* the
// whole way, 239 to 94 tok/s.
//
// So smaller is not simply faster, and anyone tuning this should know which
// direction they are trading in: a smaller batch skips more of an unchanged
// conversational prompt (4.4 s to 0.8 s here) and reads a genuinely new one
// more slowly (5.2 s to 5.4 s cold at 128, 7.8 s at 32). It is a win because
// voice turns keep the persona and history fixed and change only the last
// sentence; it is not a free one.
//
// Sending num_batch explicitly also turns off Ollama's automatic choice, which
// is the point: the useful batch depends on the model and on how much of the
// prompt repeats, and the context window knows neither.
// See docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md.
func (o *OllamaBrain) requestOptions() map[string]any {
	return o.requestOptionsFor(o.contextHighWater)
}

// requestOptionsFor is requestOptions over a snapshot of the context
// high-water mark, for a caller on another goroutine.
func (o *OllamaBrain) requestOptionsFor(highWater int) map[string]any {
	opts := map[string]any{}
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		opts["num_ctx"] = o.numCtxFor(highWater)
	}
	if o.cfg != nil && o.cfg.OllamaNumBatch > 0 {
		opts["num_batch"] = o.cfg.OllamaNumBatch
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

// chat issues a chat request, retrying once without tools if the model reports
// it doesn't support them — so a non-tool model degrades to plain chat instead
// of failing the turn. The degradation is logged so "tools silently vanished"
// is never invisible.
func (o *OllamaBrain) chat(ctx context.Context, req *api.ChatRequest, fn api.ChatResponseFunc) error {
	budget := workerBudgetFromContext(ctx)
	for {
		if err := budget.AcquireRequest(ctx); err != nil {
			return err
		}
		if err := o.applyOutputBudget(req, budget); err != nil {
			return err
		}
		var input, output int
		err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
			if resp.Done {
				input, output = resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount
			}
			return fn(resp)
		})
		rejectedTools := err != nil && req.Tools != nil && modelRejectedTools(err)
		rejectedThink := err != nil && thinkEnabled(req.Think) && modelRejectedThink(err)
		if !rejectedTools && !rejectedThink {
			budget.RecordUsage(input, output)
			if limitErr := budget.Err(); limitErr != nil {
				return limitErr
			}
			return err
		}
		budget.rejectedRequest()
		if err := budget.AcquireRetry(ctx); err != nil {
			return err
		}
		if rejectedTools {
			fmt.Fprintf(os.Stderr, "obey-voice: ollama model %q rejected tools (%v); continuing without tool calling — file read/write will not run\n", o.model, err)
			req.Tools = nil
		} else {
			fmt.Fprintf(os.Stderr, "obey-voice: ollama model %q rejected think (%v); continuing with think=false\n", o.model, err)
			req.Think = ollamaThinkValue("false")
		}
	}
}

func (o *OllamaBrain) applyOutputBudget(req *api.ChatRequest, budget *WorkerBudget) error {
	if budget == nil {
		return nil
	}
	if remaining := budget.RemainingOutput(); remaining > 0 {
		if req.Options == nil {
			req.Options = map[string]any{}
		}
		if existing, ok := req.Options["num_predict"].(int); !ok || existing <= 0 || remaining < existing {
			req.Options["num_predict"] = remaining
		}
	} else if budget.Snapshot().Limits.MaxOutputTokens > 0 {
		return ErrWorkerBudgetExceeded
	}
	return nil
}

func modelRejectedTools(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not support tools")
}

func modelRejectedThink(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not support think") || strings.Contains(msg, "does not support thinking")
}
