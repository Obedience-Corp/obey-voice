package audiojob

import "context"

// Run drains narration frames and publishes only complete artifacts. emit must
// backpressure rather than drop events. V1 narration behavior stays unchanged.
func Run(ctx context.Context, c Config, r Request, s Synthesizer, p Provenance, emit func(Event) error) (Manifest, error) {
	return run(ctx, c, r, p, emit, func(ctx context.Context, stage string) (Manifest, error) { return renderNarration(ctx, r, s, p, stage) })
}

// RunText covers every requested input window. No findings are published if any
// window, terminal response, input recheck or artifact publication fails.
func RunText(ctx context.Context, c Config, r Request, s TextAnalyzer, p Provenance, emit func(Event) error) (Manifest, error) {
	return run(ctx, c, r, p, emit, func(ctx context.Context, stage string) (Manifest, error) { return renderText(ctx, r, s, p, stage) })
}
