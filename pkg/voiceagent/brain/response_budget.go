package brain

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// ErrResponseLimit is terminal for this response: automatic continuation would
// restart the very generation this limit is meant to stop.
var ErrResponseLimit = errors.New("response output limit reached; generation stopped")

type responseBudget struct {
	remaining, byteLimit, bytes, requestBytes, requestLimit int
}

func newResponseBudget(cfg *config.Config, opts StreamOptions) *responseBudget {
	if opts.workerBudget() != nil || opts.OmitTurnInstruction {
		return nil
	}
	limit := 4096
	if cfg != nil && cfg.OllamaResponseTokens > 0 {
		limit = min(cfg.OllamaResponseTokens, 131072)
	}
	// A byte fuse also stops streaming servers that ignore num_predict or omit
	// terminal usage. This is a generous transport bound, not a tokenizer.
	return &responseBudget{remaining: limit, byteLimit: limit * 16}
}

type responseBudgetContextKey struct{}

func withResponseBudget(ctx context.Context, budget *responseBudget) context.Context {
	if budget == nil {
		return ctx
	}
	return context.WithValue(ctx, responseBudgetContextKey{}, budget)
}

func responseBudgetFromContext(ctx context.Context) *responseBudget {
	budget, _ := ctx.Value(responseBudgetContextKey{}).(*responseBudget)
	return budget
}

func (b *responseBudget) apply(req *api.ChatRequest) error {
	return b.applyRequest(req, len(req.Tools) > 0)
}

func (b *responseBudget) applyRequest(req *api.ChatRequest, reserveRepair bool) error {
	if b == nil {
		return nil
	}
	if b.remaining <= 0 {
		return ErrResponseLimit
	}
	if req.Options == nil {
		req.Options = map[string]any{}
	}
	limit, ok := req.Options["num_predict"].(int)
	if !ok || limit <= 0 || limit > b.remaining {
		limit = b.remaining
	}
	// A rejected malformed tool generation can have no usage response. Reserve
	// room for the existing one-shot repair, then charge its full issued cap
	// if the provider cannot report what that failed generation consumed.
	if reserveRepair && b.remaining > 1 {
		limit = min(limit, b.remaining/2)
	}
	req.Options["num_predict"] = limit
	b.requestLimit, b.requestBytes = limit, 0
	return nil
}

func (b *responseBudget) observe(resp api.ChatResponse) error {
	if b == nil {
		return nil
	}
	n := len(resp.Message.Content) + len(resp.Message.Thinking)
	if len(resp.Message.ToolCalls) > 0 {
		encoded, _ := json.Marshal(resp.Message.ToolCalls)
		n += len(encoded)
	}
	b.bytes += n
	b.requestBytes += n
	if b.bytes > b.byteLimit {
		return ErrResponseLimit
	}
	if resp.Done {
		spent := resp.Metrics.EvalCount
		if spent <= 0 {
			spent = max(1, (b.requestBytes+3)/4)
		}
		// A smaller per-request cap (e.g. a discussion) keeps its own outcome.
		limited := resp.DoneReason == "length" && b.requestLimit == b.remaining &&
			(resp.Metrics.EvalCount <= 0 || spent >= b.requestLimit)
		b.remaining -= spent
		if limited || b.remaining < 0 {
			return ErrResponseLimit
		}
	}
	return nil
}

func (b *responseBudget) repair(req *api.ChatRequest) error {
	if b == nil {
		return nil
	}
	b.remaining -= b.requestLimit
	return b.applyRequest(req, false)
}
