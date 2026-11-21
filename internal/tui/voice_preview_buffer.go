package tui

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// completeVoicePreview prepares the entire short sample before playback. A
// preview demonstrates the voice's sound; streaming an unpaced server directly
// to the device can add underrun gaps when synthesis is slower than realtime.
func completeVoicePreview(ctx context.Context, source *audio.PCMStream) (*audio.PCMStream, error) {
	silence := source.NewStallTimer()
	defer silence.Stop()
	var samples []float32
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-silence.C():
			return nil, silence.Err()
		case chunk, ok := <-source.Frames():
			if !ok {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if err := source.Err(); err != nil {
					return nil, err
				}
				if _, err := source.WaitReady(ctx); err != nil {
					return nil, err
				}
				complete := source.Derive(ctx)
				complete.SetIncremental(false)
				if err := complete.Write(samples); err != nil {
					return nil, err
				}
				complete.Close()
				return complete, nil
			}
			if len(chunk) == 0 {
				continue
			}
			samples = append(samples, chunk...)
			silence.ReceivedFrames()
		}
	}
}
