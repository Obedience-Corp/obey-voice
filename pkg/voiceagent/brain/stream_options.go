package brain

// effectiveRouterPolicy returns the tool policy for one streamed turn.
func (o StreamOptions) effectiveRouterPolicy() RouterToolsPolicy {
	if o.RouterToolsPolicy != "" {
		return o.RouterToolsPolicy
	}
	if o.ToolsEnabled {
		return RouterToolsRouteOnly
	}
	return ""
}
