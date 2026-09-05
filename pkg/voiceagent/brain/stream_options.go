package brain

// effectiveRouterPolicy returns the tool policy for one streamed turn.
// Empty means full worker tools; the live microphone path passes route_only.
func (o StreamOptions) effectiveRouterPolicy() RouterToolsPolicy {
	return o.RouterToolsPolicy
}

// liveCLITools reports whether a Claude/Cursor/Grok turn should run native
// tools. The live microphone path is route_only: those CLIs must stay
// conversational so work does not fill the talking session. Workers pass
// legacy and keep tools. Ollama still receives ToolsEnabled plus the route
// schema on the live path — it is the classifier, not the worker.
func liveCLITools(opts StreamOptions) bool {
	if !opts.ToolsEnabled {
		return false
	}
	return opts.effectiveRouterPolicy() != RouterToolsRouteOnly
}
