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
