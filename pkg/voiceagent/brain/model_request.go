package brain

import (
	"context"
	"time"

	"github.com/ollama/ollama/api"
)

// Purposes reported for a model request. Readers treat the set as open.
const (
	PurposeWarmup        = "warmup"
	PurposeSkillCheck    = "skill_check"
	PurposeRoute         = "route"
	PurposeRouteRecovery = "route_recovery"
	PurposeReply         = "reply"
	PurposeOther         = "other"
)

// ModelRequest identifies one request as it is sent to a model provider.
type ModelRequest struct {
	Purpose  string
	Provider string
	Model    string
	Sent     time.Time
}

// ModelRequestResult is what is known when a model request finishes. A zero
// time or nil value was not measured. The durations are the provider's own
// report and are independent of the observer's wall clock.
type ModelRequestResult struct {
	Finished     time.Time
	FirstChunk   time.Time
	PromptTokens *int
	OutputTokens *int
	Load         *time.Duration
	Prefill      *time.Duration
	Generation   *time.Duration
	Err          error
}

// ModelRequestObserver is called with the caller's context when a request is
// sent. The returned func is called exactly once when that request finishes.
type ModelRequestObserver func(context.Context, ModelRequest) func(ModelRequestResult)

type modelRequestPurposeKey struct{}

func withModelRequestPurpose(ctx context.Context, purpose string) context.Context {
	return context.WithValue(ctx, modelRequestPurposeKey{}, purpose)
}

func modelRequestPurpose(ctx context.Context) string {
	if purpose, ok := ctx.Value(modelRequestPurposeKey{}).(string); ok {
		return purpose
	}
	return PurposeOther
}

// SetModelRequestObserver reports each request this brain sends to Ollama.
func (o *OllamaBrain) SetModelRequestObserver(fn ModelRequestObserver) {
	if fn == nil {
		o.requestObserver.Store(nil)
		return
	}
	o.requestObserver.Store(&fn)
}

// clientChat is the single place an Ollama chat request leaves this brain.
func (o *OllamaBrain) clientChat(ctx context.Context, req *api.ChatRequest, fn api.ChatResponseFunc) error {
	observe := o.requestObserver.Load()
	if observe == nil {
		return o.client.Chat(ctx, req, fn)
	}
	finish := (*observe)(ctx, ModelRequest{Purpose: modelRequestPurpose(ctx), Provider: "ollama", Model: req.Model, Sent: time.Now()})
	var result ModelRequestResult
	err := o.client.Chat(ctx, req, func(resp api.ChatResponse) error {
		if result.FirstChunk.IsZero() && (resp.Message.Content != "" || resp.Message.Thinking != "" || len(resp.Message.ToolCalls) > 0) {
			result.FirstChunk = time.Now()
		}
		if resp.Done {
			result.reportMetrics(resp.Metrics)
		}
		return fn(resp)
	})
	result.Finished = time.Now()
	result.Err = err
	finish(result)
	return err
}

// Ollama omits zero metrics from its responses, so zero is reported as not
// measured rather than as a measured zero.
func (r *ModelRequestResult) reportMetrics(m api.Metrics) {
	count := func(n int) *int {
		if n <= 0 {
			return nil
		}
		return new(n)
	}
	duration := func(d time.Duration) *time.Duration {
		if d <= 0 {
			return nil
		}
		return new(d)
	}
	r.PromptTokens, r.OutputTokens = count(m.PromptEvalCount), count(m.EvalCount)
	r.Load, r.Prefill, r.Generation = duration(m.LoadDuration), duration(m.PromptEvalDuration), duration(m.EvalDuration)
}

// roundPurpose labels one round of a turn's tool loop. A continuation of a
// truncated reply is reported as other.
func roundPurpose(routing, continuation bool) string {
	switch {
	case continuation:
		return PurposeOther
	case routing:
		return PurposeRoute
	default:
		return PurposeReply
	}
}
