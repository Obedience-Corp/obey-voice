package pipeline

import (
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func (m *turnMetrics) timing() *events.TurnTiming {
	origin, name := m.start, "turn_start"
	if !m.sttFinal.IsZero() {
		origin, name = m.sttFinal, "finalized_input"
	}
	milestones := func(from time.Time) events.TimingMilestones {
		elapsed := func(at time.Time) *float64 {
			if at.IsZero() {
				return nil
			}
			// Preserve negative offsets too: an early barge-in or an unexpected
			// ordering is evidence, and must not be silently clamped to zero.
			value := float64(at.Sub(from)) / float64(time.Millisecond)
			return &value
		}
		result := events.TimingMilestones{
			FinalizedInputMS: elapsed(m.sttFinal), FirstModelChunkMS: elapsed(m.firstModelChunk),
			ModelCompleteMS: elapsed(m.modelComplete), FirstAudioReadyMS: elapsed(m.firstAudioReady),
			BargeInMS: elapsed(m.bargeInAt()),
		}
		switch m.audibleSink {
		case audio.SinkLocalSpeaker:
			result.PlaybackStartMS, result.PlaybackCompleteMS = elapsed(m.playbackStart), elapsed(m.playbackComplete)
		case audio.SinkStreamClient:
			result.StreamDrainMS = elapsed(m.playbackComplete)
		}
		return result
	}
	return &events.TurnTiming{
		Origin: name, OriginOffsetMS: float64(origin.Sub(m.start)) / float64(time.Millisecond),
		Raw: milestones(m.start), Normalized: milestones(origin),
	}
}
