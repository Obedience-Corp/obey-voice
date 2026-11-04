package audio

import "context"

// PlaybackContextBinder snapshots output admission before synthesis starts.
// Implementations preserve an existing binding so retries cannot revive audio
// invalidated by Stop or output recovery.
type PlaybackContextBinder interface {
	BindPlaybackContext(context.Context) context.Context
}

type playbackGenerationKey struct{ owner any }

// WithPlaybackGeneration binds an output owner's current admission generation.
func WithPlaybackGeneration(ctx context.Context, owner any, generation uint64) context.Context {
	return context.WithValue(ctx, playbackGenerationKey{owner}, generation)
}

// PlaybackGeneration returns the admission generation for the specified owner.
func PlaybackGeneration(ctx context.Context, owner any) (uint64, bool) {
	generation, ok := ctx.Value(playbackGenerationKey{owner}).(uint64)
	return generation, ok
}

// BindPlaybackContext snapshots engines supporting output admission fencing.
func BindPlaybackContext(ctx context.Context, engine Engine) context.Context {
	if binder, ok := engine.(PlaybackContextBinder); ok {
		return binder.BindPlaybackContext(ctx)
	}
	return ctx
}
