package playback

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// BindPlaybackContext admits an attempt before provider work begins. A bound
// attempt stays bound across retries, including retries after a device reset.
func (p *Player) BindPlaybackContext(ctx context.Context) context.Context {
	if _, ok := audio.PlaybackGeneration(ctx, p); ok {
		return ctx
	}
	p.mu.Lock()
	epoch := p.epoch
	p.mu.Unlock()
	return audio.WithPlaybackGeneration(ctx, p, epoch)
}

// RebindPlaybackContext admits a new attempt on the current generation,
// replacing a binding the caller's own Stop invalidated.
func (p *Player) RebindPlaybackContext(ctx context.Context) context.Context {
	p.mu.Lock()
	epoch := p.epoch
	p.mu.Unlock()
	return audio.WithPlaybackGeneration(ctx, p, epoch)
}
