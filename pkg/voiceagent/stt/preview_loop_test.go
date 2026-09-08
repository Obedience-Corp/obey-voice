package stt

import (
	"context"
	"math"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type previewSegmenter struct{ speech bool }

func (s *previewSegmenter) AcceptWaveform(v []float32)   { s.speech = frameRMS(v) > 0.01 }
func (s *previewSegmenter) IsSpeech() bool               { return s.speech }
func (*previewSegmenter) HasSegments() bool              { return false }
func (*previewSegmenter) NextSegment() ([]float32, bool) { return nil, false }
func (s *previewSegmenter) Reset()                       { s.speech = false }
func (*previewSegmenter) Flush()                         {}

func TestPreviewEmitsWordsBeforeEOFAndFinalizesOnlyAtEOF(t *testing.T) {
	frames := &scriptedFrames{}
	for range 20 {
		frame := make([]float32, 1600)
		for i := range frame {
			frame[i] = float32(0.004 * math.Sin(float64(i)*0.1))
		}
		frames.chunks = append(frames.chunks, frame)
	}
	events := make(chan Event, 64)
	partialBeforeEOF := false
	deps := offlineLoopDeps{frames: frames, seg: &previewSegmenter{}, transcribe: func(samples []float32) (string, error) {
		if !frames.done {
			partialBeforeEOF = true
		}
		return "quiet microphone speech", nil
	}}
	runPreviewLoop(context.Background(), deps, events)
	close(events)
	var partial, final string
	for event := range events {
		switch e := event.(type) {
		case PartialTranscript:
			partial = e.Text
		case FinalTranscript:
			final = e.Text
		}
	}
	if !partialBeforeEOF || partial != "quiet microphone speech" || final != partial {
		t.Fatalf("before EOF=%v partial=%q final=%q", partialBeforeEOF, partial, final)
	}
}

func TestPreviewSilenceDoesNotInvokeDecoder(t *testing.T) {
	frames := &scriptedFrames{chunks: [][]float32{make([]float32, audio.SampleRate)}}
	events := make(chan Event, 64)
	calls := 0
	runPreviewLoop(context.Background(), offlineLoopDeps{frames: frames, seg: &previewSegmenter{}, transcribe: func([]float32) (string, error) { calls++; return "hallucination", nil }}, events)
	if calls != 0 {
		t.Fatalf("silence reached decoder %d times", calls)
	}
}

func TestPreviewGainIsBoundedAndPreservesDigitalSilence(t *testing.T) {
	got := previewSamples([]float32{0, 0.0001, -0.0001})
	if got[0] != 0 || got[1] > 0.00081 || got[2] < -0.00081 {
		t.Fatalf("gain unbounded: %v", got)
	}
}
