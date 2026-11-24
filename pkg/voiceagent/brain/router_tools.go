package brain

import (
	"log"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// RouterToolsPolicy controls which tools the live conversational brain may see.
type RouterToolsPolicy string

const (
	// RouterToolsRouteOnly exposes only the constrained route tool on the mic path.
	RouterToolsRouteOnly RouterToolsPolicy = "route_only"
	// RouterToolsDirect lets the model use its tools on the speaking turn.
	RouterToolsDirect RouterToolsPolicy = "direct"
	// RouterToolsLegacy is the historical spelling retained in worker diagnostics.
	RouterToolsLegacy RouterToolsPolicy = "legacy"
)

// JSONRouteSetting is the voice_router_tools value that keeps the route_only
// policy but has a live Ollama brain ask for the route as JSON, without tools,
// so the routing request and the reply share one prompt prefix. It is a
// setting, not a policy.
const JSONRouteSetting string = "json_route"

// JSONRouteEnabled reports whether a voice_router_tools value selects json_route.
func JSONRouteEnabled(raw string) bool {
	return strings.ToLower(strings.TrimSpace(raw)) == JSONRouteSetting
}

// SingleCallSetting is the voice_router_tools value that keeps the route_only
// policy but lets a live Ollama brain answer chat in the routing request
// itself: a text answer is the reply, and a route call hands the turn off.
const SingleCallSetting string = "single_call"

// SingleCallEnabled reports whether a voice_router_tools value selects single_call.
func SingleCallEnabled(raw string) bool {
	return strings.ToLower(strings.TrimSpace(raw)) == SingleCallSetting
}

// singleCallConfigured reports whether a route_only turn of this brain answers
// chat in its routing request. It is read from the brain's own configuration,
// the same source as the system prompt's tool surface.
func singleCallConfigured(cfg *config.Config) bool {
	return cfg != nil && SingleCallEnabled(cfg.VoiceRouterTools)
}

func onlyRouteTool(tools api.Tools) bool {
	return len(tools) == 1 && tools[0].Function.Name == "route"
}

const missingRouteInstruction = "Select the route before answering. Call route with kind=spawn for actions, finding or attaching files, and external lookups; kind=chat for conversation; kind=narrate for reading aloud. Do not substitute a promise or claimed result for the tool call."

const chatRouteAnswerInstruction = "Routing is complete for this turn: chat. Now answer the user directly in character. Do not call route again or describe your reasoning or routing decision."

var unknownRouterToolsOnce sync.Once

// NormalizeRouterToolsPolicy maps config values to a live-brain tool policy.
// Unknown values fail closed to route_only and log once.
func NormalizeRouterToolsPolicy(raw string) RouterToolsPolicy {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(RouterToolsRouteOnly), JSONRouteSetting, SingleCallSetting:
		return RouterToolsRouteOnly
	case "", string(RouterToolsDirect), string(RouterToolsLegacy):
		return RouterToolsDirect
	default:
		unknownRouterToolsOnce.Do(func() {
			log.Printf("obey-voice: unknown voice_router_tools %q; using route_only on the live brain", raw)
		})
		return RouterToolsRouteOnly
	}
}

// configuredLiveToolsPolicy includes tiered admission, which remains active even
// when direct tool execution is disabled.
func configuredLiveToolsPolicy(cfg *config.Config) RouterToolsPolicy {
	if cfg == nil {
		return RouterToolsDirect
	}
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		return RouterToolsRouteOnly
	}
	return NormalizeRouterToolsPolicy(cfg.VoiceRouterTools)
}

// RouterToolsForSession returns the tool definitions for one Think* turn.
// Only an explicit route_only policy gets the constrained live tool; empty
// and legacy both keep the full worker list so tests and workers do not
// silently lose web_search / files / shell.
func RouterToolsForSession(policy RouterToolsPolicy, catalog []skills.Skill) api.Tools {
	return routerToolsForSession(policy, catalog, nil, false)
}

func routerToolsForSession(policy RouterToolsPolicy, catalog []skills.Skill, tiers *WorkerTierAvailability, singleCall bool) api.Tools {
	if policy == RouterToolsRouteOnly {
		if singleCall {
			return singleCallRouterTools(tiers)
		}
		return liveRouterTools(tiers)
	}
	return voiceAssistantTools(catalog)
}

const backgroundWorkTool = "start_background_work"

// The worker receives the original request and its conversation context. Offer
// ownership while the task can still be run without repeating side effects.
func (opts StreamOptions) canHandOff() bool {
	return opts.BackgroundWorker && opts.ToolsEnabled && opts.RouterToolsPolicy == RouterToolsDirect &&
		!opts.Background && !opts.AgentContinuation && opts.Discussion == nil && !opts.OmitTurnInstruction && !opts.TransientHistory && !opts.RetryInput
}

func withBackgroundWork(tools api.Tools) api.Tools {
	return append(tools, api.Tool{Type: "function", Function: api.ToolFunction{
		Name:        backgroundWorkTool,
		Description: "Start the user's current request on a background worker so the conversation can continue. Choose this for multi-step investigations, downloads, long-running commands, or an explicit request to work in the background. Use direct tools for quick local operations. The worker receives the original request.",
		Parameters:  api.ToolFunctionParameters{Type: "object", Properties: orderedProps(nil)},
	}})
}

func directWorkerHandoff(calls []api.ToolCall) bool {
	for _, call := range calls {
		switch call.Function.Name {
		case backgroundWorkTool, "web_search", "fetch_url":
			return true
		}
	}
	return false
}

// A second tool round after inspecting local state is multi-step work. Any
// other completed tool might have changed state and must not be replayed.
func onlyLocalInspection(calls []api.ToolCall) bool {
	for _, call := range calls {
		switch call.Function.Name {
		case "list_files", "read_file", "read_skill", viewImageToolName:
		default:
			return false
		}
	}
	return true
}

// LiveRouterDecision reports whether live route_only tool calls should leave
// the conversational loop. spawn/narrate from the route tool, or any worker
// tool the live schema does not expose, become pipeline routes. Chat (or no
// calls) stays in the turn.
func LiveRouterDecision(policy RouterToolsPolicy, calls []api.ToolCall) string {
	if policy != RouterToolsRouteOnly {
		return ""
	}
	for _, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "route" {
			kind := ParseRouteKind(toolCallArg(call, "kind"))
			if kind == "spawn" || kind == "narrate" {
				return kind
			}
			continue
		}
		if name == "agent_delegate" || name == "agent_cancel" || name == "agent_delegations" || name == jobControlToolName {
			continue
		}
		if name != "" {
			return "spawn"
		}
	}
	return ""
}

func callsTool(calls []api.ToolCall, name string) bool {
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Name) == name {
			return true
		}
	}
	return false
}

func hasRouteCall(calls []api.ToolCall) bool {
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Name) == "route" {
			return true
		}
	}
	return false
}

func toolCallArg(call api.ToolCall, key string) string {
	args := call.Function.Arguments.ToMap()
	value, _ := args[key].(string)
	return value
}

func routeMetadata(calls []api.ToolCall) (config.WorkerTierName, string) {
	var selected config.WorkerTierName
	var selectedReason string
	found := false
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Name) != "route" {
			continue
		}
		args := call.Function.Arguments.ToMap()
		kind := ParseRouteKind(stringValue(args["kind"]))
		if kind != "spawn" && kind != "narrate" {
			continue
		}
		var tier config.WorkerTierName
		switch strings.ToLower(strings.TrimSpace(stringValue(args["tier"]))) {
		case "simple":
			tier = config.WorkerTierSimple
		case "medium":
			tier = config.WorkerTierMedium
		case "complex":
			tier = config.WorkerTierComplex
		}
		reason := strings.TrimSpace(stringValue(args["reason_code"]))
		switch reason {
		case "bounded_action", "dependent_steps", "broad_scope", "explicit_request":
		default:
			reason = ""
		}
		if found && (selected != tier || selectedReason != reason) {
			return "", ""
		}
		selected, selectedReason, found = tier, reason, true
	}
	if !found {
		return "", ""
	}
	return selected, selectedReason
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func workerTierAvailability(cfg *config.Config) *WorkerTierAvailability {
	return WorkerTierAvailabilityForConfig(cfg)
}

// WorkerTierAvailabilityForConfig exposes only tiers that are enabled and
// statically admissible. Installed-model/catalog checks remain dispatch-time.
func WorkerTierAvailabilityForConfig(cfg *config.Config) *WorkerTierAvailability {
	if cfg == nil || cfg.VoiceWorkers == nil || cfg.VoiceWorkers.Mode != config.WorkerModeTiered {
		return nil
	}
	available := &WorkerTierAvailability{}
	for _, name := range []config.WorkerTierName{config.WorkerTierSimple, config.WorkerTierMedium, config.WorkerTierComplex} {
		_, err := config.ResolveWorkerPlan(cfg, name, LocalWorkerCapabilities{})
		ok := err == nil
		switch name {
		case config.WorkerTierSimple:
			available.Simple = ok
		case config.WorkerTierMedium:
			available.Medium = ok
		case config.WorkerTierComplex:
			available.Complex = ok
		}
	}
	return available
}
