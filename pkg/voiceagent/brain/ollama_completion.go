package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ollama/ollama/api"
)

// Complete performs one private, tools-free structured request. It deliberately
// does not call receiveDelegationResults or touch o.history.
func (o *OllamaBrain) Complete(ctx context.Context, req CompletionRequest) (CompletionEnvelope, error) {
	ctx, cancel := context.WithTimeout(ctx, CompletionDeadline)
	defer cancel()
	onUsage := req.OnUsage
	req = BuildCompletionRequest(req.Input, req.Speaker, req.Conversation, req.Results)
	req.OnUsage = onUsage
	if err := ctx.Err(); err != nil {
		return CompletionEnvelope{}, err
	}
	o.stateMu.Lock()
	system := o.fullSystemPrompt
	options := o.requestOptionsFor(o.contextHighWater)
	model, keepAlive, think := o.model, o.keepAlive, o.resolvedThink()
	o.stateMu.Unlock()
	if options == nil {
		options = map[string]any{}
	}
	options["num_predict"] = CompletionOutputTokens
	stream := false
	format := json.RawMessage(`{"type":"object","required":["answer","updates"],"properties":{"answer":{"type":"string"},"updates":{"type":"array","maxItems":3,"items":{"type":"object","required":["result_id","text"],"properties":{"result_id":{"type":"string"},"text":{"type":"string"}},"additionalProperties":false}}},"additionalProperties":false}`)
	request := &api.ChatRequest{
		Model:    model,
		Messages: []api.Message{{Role: "system", Content: system + o.conversationIdentityGuidance()}, {Role: "user", Content: completionPrompt(req)}},
		Tools:    nil, Stream: &stream, Format: format, Options: options, KeepAlive: keepAlive, Think: think,
	}
	var raw string
	var prefill, generated int
	err := o.client.Chat(ctx, request, func(response api.ChatResponse) error {
		raw = response.Message.Content
		prefill, generated = response.Metrics.PromptEvalCount, response.Metrics.EvalCount
		return nil
	})
	if err != nil {
		return CompletionEnvelope{}, fmt.Errorf("ollama completion: %w", err)
	}
	if req.OnUsage != nil {
		req.OnUsage(prefill, generated)
	}
	var envelope CompletionEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &envelope); err != nil {
		return CompletionEnvelope{}, fmt.Errorf("ollama completion: invalid envelope: %w", err)
	}
	if err := envelope.Validate(req); err != nil {
		return CompletionEnvelope{}, err
	}
	return envelope, nil
}

func (o *OllamaBrain) AppendAssistantResponse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	o.appendMessage(api.Message{Role: "assistant", Content: text}, nil)
	if o.cfg != nil && o.cfg.MaxHistory > 0 {
		o.trimHistory()
	}
	return nil
}

func (o *OllamaBrain) AppendAssistantResponseWithIdentity(text string, identity TurnIdentity) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("brain: empty assistant response")
	}
	o.appendMessage(api.Message{Role: "assistant", Content: text}, &identity)
	if o.cfg != nil && o.cfg.MaxHistory > 0 {
		o.trimHistory()
	}
	return nil
}

var _ CompletionProvider = (*OllamaBrain)(nil)
var _ HistoryAppender = (*OllamaBrain)(nil)
