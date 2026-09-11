package netapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestTimingWireKeepsMillisecondsOriginsAndUnknownBoundaries(t *testing.T) {
	raw, normalized := 4125.5, 1125.5
	data, err := json.Marshal(encodeEvent(events.TurnMetrics{
		ModelCompleteElapsed: 4125500 * time.Microsecond,
		Envelope:             &events.ResponseEnvelope{ResponseID: "response", PersonaID: "santa"},
		Timing: &events.TurnTiming{Origin: "finalized_input", OriginOffsetMS: 3000,
			Raw:        events.TimingMilestones{ModelCompleteMS: &raw},
			Normalized: events.TimingMilestones{ModelCompleteMS: &normalized}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	timing := got["timing"].(map[string]any)
	if timing["origin"] != "finalized_input" || timing["origin_offset_ms"] != float64(3000) {
		t.Fatalf("origin = %+v", timing)
	}
	norm := timing["normalized"].(map[string]any)
	if norm["model_complete_ms"] != normalized || norm["playback_complete_ms"] != nil {
		t.Fatalf("milestones = %+v", norm)
	}
	if got["model_complete_ms"] != float64(4125) {
		t.Fatalf("legacy model milliseconds = %v", got["model_complete_ms"])
	}
	if got["response_envelope"].(map[string]any)["response_id"] != "response" {
		t.Fatal("response correlation lost")
	}
}
