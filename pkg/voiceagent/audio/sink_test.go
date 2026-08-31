package audio

import "testing"

func TestSinkKindConstants(t *testing.T) {
	if SinkNone != "none" || SinkLocalSpeaker != "local_speaker" || SinkStreamClient != "stream_client" {
		t.Fatalf("sink constants drifted: none=%q local=%q stream=%q", SinkNone, SinkLocalSpeaker, SinkStreamClient)
	}
}
