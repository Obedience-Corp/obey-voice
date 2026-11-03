package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// LiveRouteClassifier supplies private inference for harnesses without a host
// route tool. It must not resume the talking session or change its history.
type LiveRouteClassifier interface {
	ClassifyLiveRoute(context.Context, LiveRouteRequest) (StreamResult, error)
}

type LiveRouteRequest struct {
	Text         string
	Conversation []Turn
	WorkerTiers  *WorkerTierAvailability
}

// HarnessRouting is request-local host guidance, never persisted as user text.
type HarnessRouting struct{ WorkersAvailable bool }

func harnessRoutingContext(cfg *config.Config, opts StreamOptions) string {
	if opts.HarnessRouting == nil {
		return ""
	}
	return "\n\n" + systemPrompt(cfg, "harness-routing-chat", map[string]string{
		"workers_available": fmt.Sprint(opts.HarnessRouting.WorkersAvailable),
	})
}

type routeInference func(context.Context, BatchRequest) (string, error)

func classifyHarnessRoute(ctx context.Context, cfg *config.Config, req LiveRouteRequest, infer routeInference) (StreamResult, error) {
	ctx, cancel := context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(cfg))
	defer cancel()
	if err := ctx.Err(); err != nil {
		return StreamResult{}, err
	}
	// Reuse the live tool's schema and strict decoder. No provider gets native
	// execution authority just to decide whether a worker is needed.
	route := liveRouterTools(req.WorkerTiers)[0].Function
	schema, err := json.Marshal(route.Parameters)
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode route schema: %w", err)
	}
	var history []AddressMessage
	for _, turn := range req.Conversation {
		account := turn.Role
		if account == "samantha" {
			account = "assistant"
		}
		if account != "user" && account != "assistant" {
			continue
		}
		history = append(history, AddressMessage{Name: turn.PersonaDisplayName, Account: account, Text: turn.Content})
	}
	payload, err := json.Marshal(struct {
		Conversation []AddressMessage `json:"conversation"`
		Text         string           `json:"latest_user_request"`
		Now          string           `json:"current_time"`
	}{history, req.Text, time.Now().Format(time.RFC3339)})
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode route context: %w", err)
	}
	// Fail visibly rather than silently discarding earlier user constraints.
	if len(payload) > 128<<10 {
		return StreamResult{}, fmt.Errorf("harness route context exceeds 128 KiB")
	}
	text, err := infer(ctx, BatchRequest{
		SystemPrompt: systemPrompt(cfg, "harness-routing", map[string]string{"route_description": route.Description, "route_schema": string(schema)}),
		Text:         string(payload),
	})
	if err != nil {
		return StreamResult{}, fmt.Errorf("classify harness route: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return StreamResult{}, err
	}
	args, err := decodeRouteRecovery(api.ChatResponse{Done: true, Message: api.Message{Content: text}}, route.Parameters)
	if err != nil {
		return StreamResult{}, fmt.Errorf("classify harness route: %w", err)
	}
	call := api.ToolCall{Function: api.ToolCallFunction{Name: "route", Arguments: args}}
	tier, reason := routeMetadata([]api.ToolCall{call})
	kind := strings.TrimSpace(stringValue(args.ToMap()["kind"]))
	if kind == "chat" {
		kind = ""
	}
	return StreamResult{Route: kind, RouteTier: tier, RouteReasonCode: reason}, nil
}
