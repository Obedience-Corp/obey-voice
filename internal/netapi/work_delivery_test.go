package netapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestWorkDeliveryWireCarriesOriginalAttempt(t *testing.T) {
	d := events.WorkDelivery{Scope: work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}, AttemptID: "original", TurnID: "turn", CaptureEpoch: 9, Stage: "audio.delivery", ResultIDs: []string{"result"}, Reason: "remote_playback_unconfirmed", Mode: "voice", Segments: 1}
	b, err := marshalEvent(d)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Delivery events.WorkDelivery `json:"delivery"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Delivery, d) {
		t.Fatalf("delivery wire=%s", b)
	}
}
