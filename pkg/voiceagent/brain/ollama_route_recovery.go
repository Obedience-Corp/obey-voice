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
	capNumPredict(req.Options, routeDecisionTokens)
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
		decodeErr = fmt.Errorf("%w %s", decodeErr, routeDecisionDiagnostics(response))
	}
	// Forward usage/completion metadata even for a rejected decision, but never
	// expose the private JSON (or a server's unexpected prose) as speech.
	response.Message = api.Message{Role: "assistant"}
	if decodeErr == nil {
		response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}
		// The private decision is complete even if the server kept generating
		// whitespace until num_predict. Downstream loops must see a completed
		// route, not mistake it for a truncated conversational answer.
		if response.DoneReason == "length" {
			response.DoneReason = "stop"
		}
	}
	if err := fn(response); err != nil {
		return err
	}
	if decodeErr != nil {
		return fmt.Errorf("recover missing route: %w", decodeErr)
	}
	return nil
}

// routeDecisionTokens caps a private route decision with thinking off.
const routeDecisionTokens = 256

// capNumPredict sets num_predict to limit unless the request already carries a
// smaller positive limit, in any numeric form a caller may have used.
func capNumPredict(options map[string]any, limit int) {
	var n float64
	switch v := options["num_predict"].(type) {
	case int:
		n = float64(v)
	case int32:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	case float32:
		n = float64(v)
	case float64:
		n = v
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			options["num_predict"] = limit
			return
		}
		n = f
	default:
		options["num_predict"] = limit
		return
	}
	if !(n >= 1 && n <= float64(limit)) {
		options["num_predict"] = limit
	}
}

// routeDecisionDiagnostics describes a rejected route reply without repeating
// its text.
func routeDecisionDiagnostics(response api.ChatResponse) string {
	return fmt.Sprintf("(done=%t, reason=%q, generated_tokens=%d, reply_bytes=%d, json_complete=%t, thinking_bytes=%d, tool_calls=%d)",
		response.Done, response.DoneReason, response.EvalCount,
		len(response.Message.Content), json.Valid([]byte(response.Message.Content)), len(response.Message.Thinking), len(response.Message.ToolCalls))
}

func decodeRouteRecovery(response api.ChatResponse, schema api.ToolFunctionParameters) (args api.ToolCallFunctionArguments, resultErr error) {
	args = api.NewToolCallFunctionArguments()
	defer func() {
		if resultErr != nil && response.DoneReason == "length" {
			resultErr = fmt.Errorf("route response exhausted its output budget: %w", resultErr)
		}
	}()
	switch {
	case !response.Done:
		return args, errors.New("route response ended without completion")
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
		values = truncatedChatDecision(response)
		if values == nil {
			return args, fmt.Errorf("decode route decision: %w", err)
		}
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

// truncatedChatDecision recovers a decision cut off at the output limit after
// it had already chosen chat. Small models write kind and then ramble inside an
// optional free-text field until the limit, so the object never closes. Chat
// starts no work and needs no other field, which makes the completed kind
// enough; a cut-off spawn or narrate decision is still rejected, because its
// goal or source is what would be acted on.
func truncatedChatDecision(response api.ChatResponse) map[string]json.RawMessage {
	if response.DoneReason != "length" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(response.Message.Content))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		key, ok := keyTok.(string)
		if err != nil || !ok {
			return nil
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil
		}
		if key == "kind" {
			var kind string
			if json.Unmarshal(raw, &kind) != nil || kind != "chat" {
				return nil
			}
			return map[string]json.RawMessage{"kind": raw}
		}
	}
	return nil
}
