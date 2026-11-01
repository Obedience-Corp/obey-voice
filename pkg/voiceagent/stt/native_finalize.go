package stt

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type nativeFinalizer struct {
	buffer     nativeCaptureBuffer
	maxSamples int
	reason     string
	accept     func(context.Context, []float32, []utterance.Span, time.Time, time.Time, string) error
}
type rangedSegmenter interface {
	NextSegmentRange() ([]float32, int, bool)
}

func (n *nativeFinalizer) finalize(ctx context.Context, seg segmenter, minimum int, floor float64, final bool, events chan<- Event) bool {
	ranged, ok := seg.(rangedSegmenter)
	if !ok {
		sendEvent(ctx, events, Failure{Err: fmt.Errorf("native capture segment ranges unavailable")})
		return true
	}
	lo, hi := -1, 0
	for {
		samples, start, ok := ranged.NextSegmentRange()
		if !ok {
			break
		}
		if len(samples) == 0 {
			continue
		}
		if start < 0 || start > n.buffer.fed-len(samples) {
			sendEvent(ctx, events, Failure{Err: utterance.ErrIncomplete})
			return true
		}
		if lo < 0 || start < lo {
			lo = start
		}
		hi = max(hi, start+len(samples))
	}
	if lo < 0 {
		if final {
			sendEvent(ctx, events, Timeout{})
			return true
		}
		return false
	}
	limit := n.maxSamples
	if limit == 0 {
		limit = utterance.MaxSamples
	}
	if hi-lo > limit {
		sendEvent(ctx, events, Failure{Err: utterance.ErrIncomplete})
		return true
	}
	samples, spans, started, ended, err := n.buffer.slice(lo, hi)
	if err != nil {
		sendEvent(ctx, events, Failure{Err: err})
		return true
	}
	if len(samples) < minimum || !audibleSpeechAbove(samples, floor) {
		if final {
			sendEvent(ctx, events, Timeout{})
			return true
		}
		return false
	}
	if err := n.accept(ctx, samples, spans, started, ended, n.reason); err != nil {
		sendEvent(ctx, events, Failure{Err: err})
	}
	return true
}
