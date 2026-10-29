package stt

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

func TestOfflineLoopKeepsAudibleShortReplies(t *testing.T) {
	for _, text := range []string{"yes", "no", "do it", "don't do it"} {
		t.Run(text, func(t *testing.T) {
			samples := constFrame(2400, .08) // 150ms, below the old hard 300ms floor.
			deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: samples}, policy: endpoint.Policy{MinSpeech: 100 * time.Millisecond}, transcribe: func(got []float32) (string, error) {
				if len(got) != len(samples) {
					t.Fatal("short audio changed")
				}
				return text, nil
			}}
			got, ok := terminal(runLoop(t.Context(), deps)).(FinalTranscript)
			if !ok || got.Text != text {
				t.Fatalf("short reply=%+v", got)
			}
		})
	}
}

func TestShortReplyFloorKeepsSilenceAndBlipRejection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []float32
		minimum time.Duration
	}{
		{"silence", make([]float32, 8000), 100 * time.Millisecond},
		{"blip", constFrame(800, .08), 100 * time.Millisecond},
		{"configured higher floor", constFrame(2400, .08), 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := offlineLoopDeps{frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}}, seg: &fakeSegmenter{speech: true, flushSeg: tc.samples}, policy: endpoint.Policy{MinSpeech: tc.minimum}, transcribe: func([]float32) (string, error) { t.Fatal("rejected audio reached decoder"); return "yes", nil }}
			if _, ok := terminal(runLoop(t.Context(), deps)).(FinalTranscript); ok {
				t.Fatal("noise became a reply")
			}
		})
	}
}
