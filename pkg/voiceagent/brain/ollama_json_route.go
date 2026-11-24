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

// jsonRouteThinkingTokens caps the route request when thinking is on. Ollama
// counts thinking against num_predict, so the 256-token decision cap could end
// a model's reasoning before it writes the object.
const jsonRouteThinkingTokens = 1024

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
	limit := routeDecisionTokens
	if thinkEnabled(req.Think) {
		limit = jsonRouteThinkingTokens
	}
	capNumPredict(req.Options, limit)
	req.Messages = slices.Clone(original.Messages)
	appendToNewestUser(req.Messages, "\n\n"+systemPrompt(o.cfg, "route-recovery", nil)+"\n"+route.Description+"\nJSON schema: "+string(schema))
	if err := o.messagesBudgetError(req.Messages, nil); err != nil {
		return err
	}
	// A first attempt: unlike the recovery call, it does not draw on the retry budget.
	ctx, cancel := context.WithTimeout(withModelRequestPurpose(ctx, PurposeRoute), config.PersonaRoutingTimeoutOf(o.cfg))
	defer cancel()
	var response api.ChatResponse
	if err := o.chat(ctx, &req, func(resp api.ChatResponse) error {
		response = resp
		return nil
	}); err != nil {
		return fmt.Errorf("json route: %w", err)
	}
	// A job_control call is never offered here; it is passed on so the turn
	// refuses it instead of reading it as a malformed route.
	if callsTool(response.Message.ToolCalls, jobControlToolName) {
		return fn(response)
	}
	args, decodeErr := decodeRouteRecovery(response, route.Parameters)
	if decodeErr != nil {
		decodeErr = fmt.Errorf("%w %s", decodeErr, routeDecisionDiagnostics(response))
	}
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

func (o *OllamaBrain) finishStreamRoute(sess *toolSession, toolCalls []api.ToolCall, kind string, jsonRouting, inspectedLocally bool) StreamResult {
	tier, reason := routeMetadata(toolCalls)
	if !jsonRouting && (sess.onStart != nil || sess.onEnd != nil) {
		decision := sess.describe("route", kind, "")
		begun := time.Now()
		if sess.onStart != nil {
			sess.onStart(decision)
		}
		if sess.onEnd != nil {
			elapsed := time.Since(begun)
			sess.onEnd(ToolResult{ToolCall: decision, Preview: kind, Elapsed: elapsed, Outcome: &events.ToolOutcome{
				Status: events.ToolOutcomeSucceeded, InputSummary: kind, OutputPreview: kind, Elapsed: elapsed,
			}})
		}
	}
	// The routed history writer owns an untouched user line.
	// After a local inspection, keep its user/tool exchange
	// intact; the writer appends only the acknowledgment.
	if n := len(o.history); n > 0 && !inspectedLocally {
		if len(o.canonical) > 0 && len(o.working) > 0 {
			o.stopped.routed = &ollamaRoutedLine{canonical: o.canonical[len(o.canonical)-1], working: o.working[len(o.working)-1], message: o.history[n-1]}
		}
		o.history = o.history[:n-1]
		if len(o.canonical) > 0 {
			o.canonical = o.canonical[:len(o.canonical)-1]
		}
		if len(o.working) > 0 {
			o.working = o.working[:len(o.working)-1]
		}
	}
	return StreamResult{Route: kind, RouteTier: tier, RouteReasonCode: reason}
}

func (sess *toolSession) checkStreamRoute(ctx context.Context, routing, jsonRouting, complete bool, toolCalls []api.ToolCall, started time.Time, err error) error {
	var routeRejected error
	if jsonRouting && errors.Is(err, errRouteRejected) {
		routeRejected, err = err, nil
	}
	if err == nil && routing && !complete {
		err = fmt.Errorf("routing response ended before completion")
	}
	if jsonRouting && ctx.Err() == nil {
		failure := routeRejected
		if failure == nil {
			failure = err
		}
		sess.reportRoute(toolCalls, failure, time.Since(started))
	}
	return err
}
