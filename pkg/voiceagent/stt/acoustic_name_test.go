package stt

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
)

func TestOfflineAcousticEvidenceUsesCompleteAdmittedAudio(t *testing.T) {
	samples := longSpeech()
	original := append([]float32(nil), samples...)
	const text = "Oh bay, explain the whole command please."
	calls := 0
	deps := offlineLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true, flushSeg: samples},
		transcribe: func(got []float32) (string, error) {
			if !reflect.DeepEqual(got, original) {
				t.Fatal("STT audio changed")
			}
			return text, nil
		},
		acousticName: func(_ context.Context, got []float32) (kws.Hit, error) {
			calls++
			if !reflect.DeepEqual(got, original) {
				t.Fatal("detector lost audio")
			}
			return kws.Hit{Name: "Obey", By: 900 * time.Millisecond}, nil
		},
	}
	got, ok := terminal(runLoop(t.Context(), deps)).(FinalTranscript)
	if !ok || got.Text != text || got.AcousticName != "Obey" || got.AcousticNameBy != 900*time.Millisecond || calls != 1 {
		t.Fatalf("final=%+v calls=%d", got, calls)
	}
	typed := ToTyped(got)
	if typed.Text != text || typed.AcousticName != "Obey" || typed.AcousticNameBy != 900*time.Millisecond {
		t.Fatalf("typed=%+v", typed)
	}
}

func TestOfflineAcousticEvidenceExcludedFromRejectedSpeech(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []float32
		text    string
	}{
		{"too short", speechFrame(), "Obey"}, {"empty transcription", longSpeech(), ""}, {"inaudible", make([]float32, 5000), "Obey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: tc.samples}, transcribe: func([]float32) (string, error) { return tc.text, nil }, acousticName: func(context.Context, []float32) (kws.Hit, error) {
				t.Fatal("detected rejected audio")
				return kws.Hit{}, nil
			}}
			if got := terminal(runLoop(t.Context(), deps)); got.sttEvent() != "timeout" {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestOfflineAcousticFailureNeverEmitsTranscript(t *testing.T) {
	for _, err := range []error{context.Canceled, errors.New("detector unavailable")} {
		deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: longSpeech()}, transcribe: func([]float32) (string, error) { return "Oh bay", nil }, acousticName: func(context.Context, []float32) (kws.Hit, error) { return kws.Hit{Name: "Obey"}, err }}
		got, ok := terminal(runLoop(t.Context(), deps)).(Failure)
		if !ok || !errors.Is(got.Err, err) {
			t.Fatalf("got=%+v", got)
		}
	}
}
