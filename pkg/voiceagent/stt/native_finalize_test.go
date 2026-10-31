package stt

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type nativeTestSegmenter struct {
	fakeSegmenter
	offset int
}

func (s *nativeTestSegmenter) NextSegmentRange() ([]float32, int, bool) {
	samples, ok := s.NextSegment()
	start := s.offset
	s.offset += len(samples)
	return samples, start, ok
}
func TestNativeOfflineLoopFinalizesAudioWithoutDecoderOrTranscript(t *testing.T) {
	samples := longSpeech()
	accepted := false
	final := &nativeFinalizer{accept: func(_ context.Context, got []float32, _ []utterance.Span, _, _ time.Time, reason string) error {
		if len(got) != len(samples) || reason != "source_exhausted" {
			t.Errorf("range=%d reason=%q", len(got), reason)
		}
		accepted = true
		return nil
	}}
	deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{samples}}, seg: &nativeTestSegmenter{fakeSegmenter: fakeSegmenter{speech: true, flushSeg: samples}}, policy: endpoint.Policy{}, native: final, transcribe: func([]float32) (string, error) { t.Fatal("native path called decoder"); return "", nil }}
	events := runLoop(context.Background(), deps)
	if !accepted {
		t.Fatalf("audio not accepted: %v", events)
	}
	for _, event := range events {
		switch event.(type) {
		case FinalTranscript, PartialTranscript:
			t.Fatal("fabricated text event")
		}
	}
}
func TestNativeOfflineLoopSilenceNeverCallsModelOrDecoder(t *testing.T) {
	called := false
	deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{make([]float32, 4800)}}, seg: &nativeTestSegmenter{}, native: &nativeFinalizer{accept: func(context.Context, []float32, []utterance.Span, time.Time, time.Time, string) error {
		called = true
		return nil
	}}}
	events := runLoop(context.Background(), deps)
	if _, ok := terminal(events).(Timeout); !ok || called {
		t.Fatal(events, called)
	}
}

func TestNativeShortMidstreamSegmentKeepsListening(t *testing.T) {
	n := &nativeFinalizer{}
	n.buffer.append(audio.Frame{Samples: []float32{0.1}, SampleRate: 16000, Channels: 1})
	seg := &nativeTestSegmenter{fakeSegmenter: fakeSegmenter{segments: [][]float32{{0.1}}}}
	events := make(chan Event, 4)
	if n.finalize(context.Background(), seg, 4800, 0.001, false, events) {
		t.Fatal("short midstream range ended capture")
	}
	if len(events) != 0 {
		t.Fatal("short range emitted terminal")
	}
}
func TestNativeConfiguredCapFailsInsteadOfAnsweringPrefix(t *testing.T) {
	for _, minimum := range []time.Duration{0, time.Hour} {
		accepted := false
		deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{longSpeech(), longSpeech()}}, seg: &nativeTestSegmenter{fakeSegmenter: fakeSegmenter{speech: true, flushSeg: longSpeech()}}, policy: endpoint.Policy{MaxUtterance: time.Nanosecond, MinSpeech: minimum}, native: &nativeFinalizer{accept: func(context.Context, []float32, []utterance.Span, time.Time, time.Time, string) error {
			accepted = true
			return nil
		}}}
		got := runLoop(context.Background(), deps)
		failure, ok := terminal(got).(Failure)
		if !ok || !errors.Is(failure.Err, utterance.ErrIncomplete) || accepted {
			t.Fatal(got, accepted)
		}
	}
}

func TestNativeFiniteEOFEnforcesConfiguredSampleCap(t *testing.T) {
	for _, limit := range []int{1600, 4800} {
		accepted := false
		samples := longSpeech()
		deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{samples}}, seg: &nativeTestSegmenter{fakeSegmenter: fakeSegmenter{speech: true, flushSeg: samples}}, native: &nativeFinalizer{maxSamples: limit, accept: func(context.Context, []float32, []utterance.Span, time.Time, time.Time, string) error {
			accepted = true
			return nil
		}}}
		got := runLoop(context.Background(), deps)
		if limit < 4800 {
			failure, ok := terminal(got).(Failure)
			if !ok || !errors.Is(failure.Err, utterance.ErrIncomplete) || accepted {
				t.Fatal(got, accepted)
			}
		} else if !accepted {
			t.Fatal("exact bounded EOF was rejected", got)
		}
	}
}
