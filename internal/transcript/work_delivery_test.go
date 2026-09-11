package transcript

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func TestDeliveryEvidencePersistsAndSurvivesDeltaPressure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	w.Attach(bus)
	d := events.WorkDelivery{Scope: work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}, AttemptID: "a", TurnID: "t", CaptureEpoch: 17, Stage: "announcement.held", ResultIDs: []string{"result-1"}, Reason: "playback_failed", Mode: "voice", Segments: 2}
	bus.Emit(d)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var got *events.WorkDelivery
	for _, r := range readRecords(t, path) {
		if r.Type == "work_delivery" {
			got = r.Delivery
		}
	}
	if !reflect.DeepEqual(got, &d) {
		t.Fatalf("saved receipt=%+v want=%+v", got, d)
	}
	q := fitTranscriptQueue([]record{{Type: "work_delivery", Delivery: &d}, {Type: "agent_delta", Text: "noise"}, {Type: "work_state_changed", Work: &work.Snapshot{Scope: d.Scope}}}, 2)
	if len(q) != 2 || q[0].Delivery == nil || q[1].Work == nil {
		t.Fatalf("delta displaced work evidence: %+v", q)
	}
}
