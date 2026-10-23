package openaispeech

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func newTestStream(ctx context.Context) *audio.PCMStream {
	s := audio.NewPCMStream(ctx)
	_ = s.SetSampleRate(SampleRate)
	return s
}
