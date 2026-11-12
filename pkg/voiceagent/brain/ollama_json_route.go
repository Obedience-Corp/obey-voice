package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// errRouteRejected marks a JSON route reply that did not decode to a valid
// route. The turn treats it like a missing route call.
var errRouteRejected = errors.New("route decision rejected")

// jsonRouteEligible reports whether a round can decide its route privately as
// JSON: the round offers only the route schema and its request ends with a
// user message the instruction can follow.
func jsonRouteEligible(tools api.Tools, messages []api.Message) bool {
	return onlyRouteTool(tools) && len(messages) > 0 && messages[len(messages)-1].Role == "user"
}

// appendToNewestUser adds text to the end of the last user message. Keeping
// per-round text at the very end leaves every earlier token unchanged.
func appendToNewestUser(messages []api.Message, text string) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			messages[i].Content += text
			return
		}
	}
}

// chatJSONRoute asks for the route as JSON on the reply round's own messages
// with no tools, so the reply that follows reuses the same prompt prefix. The
// instruction ends the request: a system message would be merged into the
// leading system block and change the prefix. Only a validated decision
// reaches the caller, as a route tool call; the reply text is never forwarded.
func (o *OllamaBrain) chatJSONRoute(ctx context.Context, original *api.ChatRequest, fn api.ChatResponseFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !jsonRouteEligible(original.Tools, original.Messages) {
		return errors.New("json route requires only the route schema and a final user message")
	}
	route := original.Tools[0].Function
	schema, err := json.Marshal(route.Parameters)
	if err != nil {
		return fmt.Errorf("encode route schema: %w", err)
	}
	req := *original
	req.Tools = nil
	req.Format = schema
	stream := false
	req.Stream = &stream
	req.Options = maps.Clone(original.Options)
	if req.Options == nil {
		req.Options = map[string]any{}
	}
	req.Options["temperature"] = 0
	if limit, ok := req.Options["num_predict"].(int); !thinkEnabled(req.Think) && (!ok || limit <= 0 || limit > 256) {
		req.Options["num_predict"] = 256
	}
	req.Messages = slices.Clone(original.Messages)
	appendToNewestUser(req.Messages, "\n\n"+systemPrompt(o.cfg, "route-recovery", nil)+"\n"+route.Description+"\nJSON schema: "+string(schema))
	if err := o.messagesBudgetError(req.Messages, nil); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(o.cfg))
	defer cancel()
	var response api.ChatResponse
	if err := o.chat(ctx, &req, func(resp api.ChatResponse) error {
		response = resp
		return nil
	}); err != nil {
		return fmt.Errorf("json route: %w", err)
	}
	args, decodeErr := decodeRouteRecovery(response, route.Parameters)
	response.Message = api.Message{Role: "assistant"}
	if decodeErr == nil {
		response.Message.ToolCalls = []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: args}}}
		if response.DoneReason == "length" {
			response.DoneReason = "stop"
		}
	}
	if err := fn(response); err != nil {
		return err
	}
	if decodeErr != nil {
		return fmt.Errorf("%w: %w", errRouteRejected, decodeErr)
	}
	return nil
}

// reportRoute publishes a privately decided route as a route tool event, so
// the turn shows the outcome and how long the decision took. A rejected
// decision is reported as a failed route.
func (s *toolSession) reportRoute(calls []api.ToolCall, rejected error, elapsed time.Duration) {
	if s.onStart == nil && s.onEnd == nil {
		return
	}
	kind := "rejected"
	if rejected == nil {
		kind = "chat"
		for _, call := range calls {
			if call.Function.Name == "route" {
				kind = ParseRouteKind(toolCallArg(call, "kind"))
				break
			}
		}
	}
	call := s.describe("route", kind, "")
	if s.onStart != nil {
		s.onStart(call)
	}
	if s.onEnd == nil {
		return
	}
	if rejected != nil {
		outcome := failedToolOutcome(kind, rejected.Error())
		outcome.Elapsed = elapsed
		s.onEnd(ToolResult{ToolCall: call, Err: "error: " + rejected.Error(), Elapsed: elapsed, Outcome: outcome})
		return
	}
	s.onEnd(ToolResult{ToolCall: call, Preview: kind, Elapsed: elapsed, Outcome: &events.ToolOutcome{
		Status: events.ToolOutcomeSucceeded, InputSummary: kind, OutputPreview: kind, Elapsed: elapsed,
	}})
}
