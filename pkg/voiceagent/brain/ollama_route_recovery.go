package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// chatRouteRecovery uses the existing route schema without native tool syntax.
// It replaces the one missing-route retry, not the dispatch policy. Prose from
// the undecided attempt is discarded; only a validated decision reaches the
// normal tool loop, and chat then gets a fresh, tools-free answer.
func (o *OllamaBrain) chatRouteRecovery(ctx context.Context, original *api.ChatRequest, fn api.ChatResponseFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var route *api.ToolFunction
	for i := range original.Tools {
		if original.Tools[i].Function.Name == "route" {
			route = &original.Tools[i].Function
			break
		}
	}
	if route == nil {
		return errors.New("route recovery requires the route schema")
	}
	schema, err := json.Marshal(route.Parameters)
	if err != nil {
		return fmt.Errorf("encode route recovery schema: %w", err)
	}
	req := *original
	req.Tools = nil
	req.Format = schema
	stream := false
	req.Stream = &stream
	req.Think = &api.ThinkValue{Value: false}
	req.Options = maps.Clone(original.Options)
	if req.Options == nil {
		req.Options = map[string]any{}
	}
	req.Options["temperature"] = 0
	if limit, ok := req.Options["num_predict"].(int); !ok || limit <= 0 || limit > 256 {
		req.Options["num_predict"] = 256
	}
	req.Messages = []api.Message{{Role: "system", Content: systemPrompt(o.cfg, "route-recovery", nil) + "\n" + route.Description}}
	for _, msg := range original.Messages {
		if msg.Role != "system" {
			req.Messages = append(req.Messages, msg)
		}
	}
	// Count the original schema along with the replacement instructions.
	if err := o.messagesBudgetError(req.Messages, original.Tools); err != nil {
		return err
	}
	if err := workerBudgetFromContext(ctx).AcquireRetry(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(o.cfg))
	defer cancel()
	var response api.ChatResponse
	err = o.chat(ctx, &req, func(resp api.ChatResponse) error {
		response = resp
		return nil
	})
	if err != nil {
		return fmt.Errorf("recover missing route: %w", err)
	}
	args, decodeErr := decodeRouteRecovery(response, route.Parameters)
	if decodeErr != nil {
		decodeErr = fmt.Errorf("%w (done=%t, reason=%q, generated_tokens=%d, reply_bytes=%d, thinking_bytes=%d, tool_calls=%d)",
			decodeErr, response.Done, response.DoneReason, response.EvalCount,
			len(response.Message.Content), len(response.Message.Thinking), len(response.Message.ToolCalls))
	}
	// Forward usage/completion metadata even for a rejected decision, but never
	// expose the private JSON (or a server's unexpected prose) as speech.
	response.Message = api.Message{Role: "assistant"}
	if decodeErr == nil {
		response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}
	}
	if err := fn(response); err != nil {
		return err
	}
	if decodeErr != nil {
		return fmt.Errorf("recover missing route: %w", decodeErr)
	}
	return nil
}

func decodeRouteRecovery(response api.ChatResponse, schema api.ToolFunctionParameters) (api.ToolCallFunctionArguments, error) {
	args := api.NewToolCallFunctionArguments()
	switch {
	case !response.Done:
		return args, errors.New("route response ended without completion")
	case response.DoneReason == "length":
		return args, errors.New("route response exhausted its output budget")
	case len(response.Message.ToolCalls) != 0:
		return args, errors.New("route response returned tool calls instead of a JSON decision")
	case len(response.Message.Content) > 4096:
		return args, errors.New("route decision exceeded the response size limit")
	case strings.TrimSpace(response.Message.Content) == "":
		if strings.TrimSpace(response.Message.Thinking) != "" {
			return args, errors.New("route response contained reasoning without a decision")
		}
		return args, errors.New("route response contained no decision")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response.Message.Content), &values); err != nil {
		return args, fmt.Errorf("decode route decision: %w", err)
	}
	for _, key := range schema.Required {
		if _, ok := values[key]; !ok {
			return args, fmt.Errorf("route decision missing %s", key)
		}
	}
	for key, raw := range values {
		property, ok := schema.Properties.Get(key)
		if !ok {
			return args, fmt.Errorf("unknown route decision field %q", key)
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return args, fmt.Errorf("route decision field %q must be a string", key)
		}
		if len(property.Enum) > 0 && !slices.Contains(property.Enum, any(*value)) {
			return args, fmt.Errorf("invalid route decision field %q", key)
		}
		args.Set(key, strings.TrimSpace(*value))
	}
	return args, nil
}
