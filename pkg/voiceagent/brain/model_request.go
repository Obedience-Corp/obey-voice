package brain

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/ollama/ollama/api"
)

// Purposes reported for a model request. Readers treat the set as open.
const (
	PurposeWarmup            = "warmup"
	PurposeSkillCheck        = "skill_check"
	PurposeRoute             = "route"
	PurposeRouteRecovery     = "route_recovery"
	PurposeReply             = "reply"
	PurposeAnnouncement      = "announcement"
	PurposeDelegationSummary = "delegation_summary"
	PurposeDiscussion        = "discussion"
	PurposeCompaction        = "compaction"
	PurposeOther             = "other"
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
	if modelRequestPurpose(ctx) != PurposeWarmup {
		o.turnStarted.Store(true)
	}
	return observedChat(ctx, &o.requestObserver, o.client, req, fn)
}

// errRequestPanicked ends the observation of a request whose response
// callback panicked; the panic itself continues unchanged.
var errRequestPanicked = errors.New("model request callback panicked")

// observedChat sends one chat request, reporting it to the observer when one
// is bound. The finish is deferred so a panicking callback still ends the
// observation.
func observedChat(ctx context.Context, observer *atomic.Pointer[ModelRequestObserver], client *api.Client, req *api.ChatRequest, fn api.ChatResponseFunc) (err error) {
	var observe *ModelRequestObserver
	if observer != nil {
		observe = observer.Load()
	}
	if observe == nil {
		return client.Chat(ctx, req, fn)
	}
	finish := (*observe)(ctx, ModelRequest{Purpose: modelRequestPurpose(ctx), Provider: "ollama", Model: req.Model, Sent: time.Now()})
	if finish == nil {
		finish = func(ModelRequestResult) {}
	}
	var result ModelRequestResult
	returned := false
	defer func() {
		result.Finished, result.Err = time.Now(), err
		if !returned {
			result.Err = errRequestPanicked
		}
		finish(result)
	}()
	err = client.Chat(ctx, req, func(resp api.ChatResponse) error {
		if result.FirstChunk.IsZero() && (resp.Message.Content != "" || resp.Message.Thinking != "" || len(resp.Message.ToolCalls) > 0) {
			result.FirstChunk = time.Now()
		}
		if resp.Done {
			result.reportMetrics(resp.Metrics)
		}
		return fn(resp)
	})
	returned = true
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
