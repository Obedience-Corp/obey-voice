package audio

import "context"

// PlaybackFence serializes a short PCM write with revocation. A validity check
// before enqueueing alone is insufficient: audio can wait in the player queue
// while the conversation binding or capture owner changes.
type PlaybackFence interface {
	Write(func()) bool
	Valid() bool
	Done() <-chan struct{}
}

// PlaybackAttempt accompanies every segment of one host-owned response.
// It is runtime authority, never persisted as part of a work snapshot.
type PlaybackAttempt struct {
	ID           string
	CaptureEpoch uint64
	Fence        PlaybackFence
	// Render is optional for legacy callers. All segments of an attempt share
	// it; consumers must use ID/CaptureEpoch to match its authority.
	Render *FirstRenderBoundary
}

type playbackAttemptKey struct{}

func WithPlaybackAttempt(ctx context.Context, attempt PlaybackAttempt) context.Context {
	return context.WithValue(ctx, playbackAttemptKey{}, attempt)
}

func PlaybackAttemptFromContext(ctx context.Context) PlaybackAttempt {
	a, _ := ctx.Value(playbackAttemptKey{}).(PlaybackAttempt)
	return a
}

// Write fences the actual sink write. Legacy turns have no fence.
func (a PlaybackAttempt) Write(fn func()) bool {
	if a.Fence == nil {
		fn()
		return true
	}
	return a.Fence.Write(fn)
}

func (a PlaybackAttempt) Valid() bool { return a.Fence == nil || a.Fence.Valid() }

// Result attributes a callback to its original response, including failures.
func (a PlaybackAttempt) Result(r PlaybackResult) PlaybackResult {
	r.AttemptID, r.CaptureEpoch = a.ID, a.CaptureEpoch
	return r
}
