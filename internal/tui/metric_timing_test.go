package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestMetricsDisplayNormalizesOneOriginAndLabelsIt(t *testing.T) {
	model, pcm, done := 1000.0, 2000.0, 6000.0
	e := events.TurnMetrics{
		STTFinalElapsed: 3 * time.Second, ModelCompleteElapsed: 4 * time.Second,
		FirstAudioReadyElapsed: 5 * time.Second, PlaybackCompleteElapsed: 6 * time.Second,
		AudibleSink: "local_speaker",
		Timing:      &events.TurnTiming{Origin: "finalized_input", Normalized: events.TimingMilestones{ModelCompleteMS: &model, FirstAudioReadyMS: &pcm, PlaybackCompleteMS: &done}},
	}
	for _, version := range []string{"current", "legacy"} {
		if version == "legacy" {
			e.Timing = nil
		}
		line := formatTurnMetrics(e)
		for _, want := range []string{"since input", "model 1.0s", "first audio 2.0s", "playback done 6.0s"} {
			if !strings.Contains(line, want) {
				t.Errorf("%s line %q missing %q", version, line, want)
			}
		}
	}
	e.AudibleSink = "stream_client"
	if line := formatTurnMetrics(e); strings.Contains(line, "playback done") {
		t.Fatalf("remote drain mislabeled: %s", line)
	}
}
