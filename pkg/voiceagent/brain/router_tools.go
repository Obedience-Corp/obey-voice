package brain

import (
	"log"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"

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
func RouterToolsForSession(policy RouterToolsPolicy, catalog []skills.Skill) api.Tools {
	if policy == RouterToolsLegacy {
		return voiceAssistantTools(catalog)
	}
	return liveRouterTools()
}
