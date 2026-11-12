package brain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// InputRetry references an existing canonical input. Hosts must serialize this
// operation with ordinary turns; it never authorizes replay of tool effects.
type InputRetry struct {
	Input           string
	Envelope        events.ResponseEnvelope
	BindingRevision uint64
}

type InputRetryProvider interface {
	FailedInputRetry() (InputRetry, bool)
	DiscardInputRetry()
}

type ollamaInputRetry struct {
	InputRetry
	history      [32]byte
	prompt       string
	skillContext string
	toolsEnabled bool
	routerPolicy RouterToolsPolicy
	routeJSON    bool
}

func (o *OllamaBrain) retryHistoryHash() [32]byte {
	data, _ := json.Marshal(o.canonical)
	return sha256.Sum256(data)
}

func (o *OllamaBrain) DiscardInputRetry() { o.inputRetry = nil }

func (o *OllamaBrain) FailedInputRetry() (InputRetry, bool) {
	r := o.inputRetry
	if r == nil || r.history != o.retryHistoryHash() || r.prompt != o.fullSystemPrompt {
		return InputRetry{}, false
	}
	return r.InputRetry, true
}

func (o *OllamaBrain) retainFailedInput(ctx context.Context, input string, opts StreamOptions, skillContext string, err error, delivered bool) {
	var outage *ProviderOutage
	if ctx.Err() != nil || !errors.As(err, &outage) || outage.Delivered || delivered || opts.Background || opts.AgentContinuation ||
		opts.Discussion != nil || opts.Utterance != nil || len(opts.Attachments) > 0 || len(o.canonical) == 0 || o.canonical[len(o.canonical)-1].Role != "user" {
		return
	}
	o.inputRetry = &ollamaInputRetry{InputRetry: InputRetry{Input: input, Envelope: events.ResponseFromContext(ctx), BindingRevision: opts.InputBindingRevision},
		history: o.retryHistoryHash(), prompt: o.fullSystemPrompt, skillContext: skillContext, toolsEnabled: opts.ToolsEnabled, routerPolicy: opts.RouterToolsPolicy, routeJSON: opts.RouteJSON}
}
