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
	// RouterToolsLegacy keeps the full voiceAssistantTools list for A/B comparison.
	RouterToolsLegacy RouterToolsPolicy = "legacy"
)

var unknownRouterToolsOnce sync.Once

// NormalizeRouterToolsPolicy maps config values to a live-brain tool policy.
// Unknown values fail closed to route_only and log once.
func NormalizeRouterToolsPolicy(raw string) RouterToolsPolicy {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RouterToolsRouteOnly):
		return RouterToolsRouteOnly
	case string(RouterToolsLegacy):
		return RouterToolsLegacy
	default:
		unknownRouterToolsOnce.Do(func() {
			log.Printf("obey-voice: unknown voice_router_tools %q; using route_only on the live brain", raw)
		})
		return RouterToolsRouteOnly
	}
}

// RouterToolsForSession returns the tool definitions for one Think* turn.
// Only an explicit route_only policy gets the constrained live tool; empty
// and legacy both keep the full worker list so tests and workers do not
// silently lose web_search / files / shell.
func RouterToolsForSession(policy RouterToolsPolicy, catalog []skills.Skill) api.Tools {
	return routerToolsForSession(policy, catalog, nil)
}

func routerToolsForSession(policy RouterToolsPolicy, catalog []skills.Skill, tiers *WorkerTierAvailability) api.Tools {
	if policy == RouterToolsRouteOnly {
		return liveRouterTools(tiers)
	}
	return voiceAssistantTools(catalog)
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
		if name == "agent_delegate" || name == "agent_cancel" || name == "agent_delegations" {
			continue
		}
		if name != "" {
			return "spawn"
		}
	}
	return ""
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
	for name, enabled := range map[config.WorkerTierName]bool{
		config.WorkerTierSimple:  cfg.VoiceWorkers.Tiers.Simple.Enabled,
		config.WorkerTierMedium:  cfg.VoiceWorkers.Tiers.Medium.Enabled,
		config.WorkerTierComplex: cfg.VoiceWorkers.Tiers.Complex.Enabled,
	} {
		tier := cfg.VoiceWorkers.Tiers.Simple
		switch name {
		case config.WorkerTierMedium:
			tier = cfg.VoiceWorkers.Tiers.Medium
		case config.WorkerTierComplex:
			tier = cfg.VoiceWorkers.Tiers.Complex
		}
		_, planErr := config.ResolveWorkerPlan(cfg, name, LocalWorkerCapabilities{})
		ok := enabled && tier.Assignment != nil && planErr == nil
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
