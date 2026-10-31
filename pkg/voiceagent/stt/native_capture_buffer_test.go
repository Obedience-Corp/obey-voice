package stt

import (
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestNativeCaptureRangePreservesPauseAndProvenance(t *testing.T) {
	start := time.Unix(100, 0)
	b := nativeCaptureBuffer{}
	for i, samples := range [][]float32{{1, 2}, {0, 0}, {3, 4}} {
		b.append(audio.Frame{Samples: samples, SampleRate: 16000, Channels: 1, StartedAt: advanceCaptureTime(start, i*2), CaptureSpans: []audio.CaptureSpan{{SampleOffset: uint64(100 + i*2), SampleCount: 2, DeviceGeneration: 1}}})
	}
	samples, spans, first, last, err := b.slice(1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 4 || samples[1] != 0 || samples[2] != 0 || samples[3] != 3 {
		t.Fatal(samples)
	}
	if len(spans) != 3 || spans[0].SampleOffset != 101 || spans[0].SampleCount != 1 || spans[2].SampleCount != 1 {
		t.Fatal(spans)
	}
	if !first.Equal(advanceCaptureTime(start, 1)) || !last.Equal(advanceCaptureTime(start, 5)) {
		t.Fatal(first, last)
	}
	b.frames[1].frame.CaptureGap.OverflowSamples = 1
	if _, _, _, _, err := b.slice(1, 5); !errors.Is(err, utterance.ErrIncomplete) {
		t.Fatal(err)
	}
}
func TestNativeCaptureRetentionFailsOldRangeInsteadOfTruncating(t *testing.T) {
	b := nativeCaptureBuffer{}
	b.append(audio.Frame{Samples: make([]float32, utterance.MaxSamples+1), SampleRate: 16000, Channels: 1})
	if _, _, _, _, err := b.slice(0, utterance.MaxSamples+1); !errors.Is(err, utterance.ErrIncomplete) {
		t.Fatal(err)
	}
	samples, _, _, _, err := b.slice(1, utterance.MaxSamples+1)
	if err != nil || len(samples) != utterance.MaxSamples {
		t.Fatal(len(samples), err)
	}
	b.reset()
	if b.fed != 0 || len(b.frames) != 0 {
		t.Fatal("reset retained prior epoch")
	}
}

func TestNativeEmptyFrameGapCannotDisappear(t *testing.T) {
	b := nativeCaptureBuffer{}
	b.append(audio.Frame{Samples: []float32{1, 2}, SampleRate: 16000, Channels: 1})
	b.append(audio.Frame{CaptureGap: audio.CaptureGap{SubscriptionDroppedCallbacks: 1}})
	if _, _, _, _, err := b.slice(0, 2); !errors.Is(err, utterance.ErrIncomplete) {
		t.Fatal("empty-frame gap disappeared", err)
	}
	b.reset()
	b.append(audio.Frame{Samples: []float32{1, 2}, SampleRate: 16000, Channels: 1})
	if _, _, _, _, err := b.slice(0, 2); err != nil {
		t.Fatal("new epoch retained old gap", err)
	}
}
