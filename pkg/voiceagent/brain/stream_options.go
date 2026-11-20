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

func (opts StreamOptions) reportStreamUsage(input, output int) {
	if opts.OnUsage != nil {
		opts.OnUsage(input, output)
	}
	if opts.OnWorkerUsage != nil {
		worker := WorkerUsage{}
		if input > 0 {
			worker.InputTokens = &input
		}
		if output > 0 {
			worker.OutputTokens = &output
		}
		opts.OnWorkerUsage(worker)
	}
}
