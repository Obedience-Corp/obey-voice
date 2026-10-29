package stt

import (
	"context"
	"errors"
	"reflect"
	"testing"
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
		acousticName: func(_ context.Context, got []float32) (string, error) {
			calls++
			if !reflect.DeepEqual(got, original) {
				t.Fatal("detector lost audio")
			}
			return "Obey", nil
		},
	}
	got, ok := terminal(runLoop(t.Context(), deps)).(FinalTranscript)
	if !ok || got.Text != text || got.AcousticName != "Obey" || calls != 1 {
		t.Fatalf("final=%+v calls=%d", got, calls)
	}
	typed := ToTyped(got)
	if typed.Text != text || typed.AcousticName != "Obey" {
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
			deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: tc.samples}, transcribe: func([]float32) (string, error) { return tc.text, nil }, acousticName: func(context.Context, []float32) (string, error) { t.Fatal("detected rejected audio"); return "", nil }}
			if got := terminal(runLoop(t.Context(), deps)); got.sttEvent() != "timeout" {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestOfflineAcousticFailureNeverEmitsTranscript(t *testing.T) {
	for _, err := range []error{context.Canceled, errors.New("detector unavailable")} {
		deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: longSpeech()}, transcribe: func([]float32) (string, error) { return "Oh bay", nil }, acousticName: func(context.Context, []float32) (string, error) { return "Obey", err }}
		got, ok := terminal(runLoop(t.Context(), deps)).(Failure)
		if !ok || !errors.Is(got.Err, err) {
			t.Fatalf("got=%+v", got)
		}
	}
}
