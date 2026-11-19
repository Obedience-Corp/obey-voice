package brain

// effectiveRouterPolicy returns the tool policy for one streamed turn.
// Empty exposes the same tools as direct mode.
func (o StreamOptions) effectiveRouterPolicy() RouterToolsPolicy {
	return o.RouterToolsPolicy
}

// liveCLITools reports whether a Claude/Cursor/Grok turn should run native
// tools. An explicitly routed speaking turn leaves tool use to its worker.
func liveCLITools(opts StreamOptions) bool {
	if !opts.ToolsEnabled {
		return false
	}
	return opts.effectiveRouterPolicy() != RouterToolsRouteOnly
}
