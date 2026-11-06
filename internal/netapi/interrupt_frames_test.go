package netapi

import (
	"reflect"
	"testing"
	"time"
)

func TestInterruptFramesWaitForBothQueues(t *testing.T) {
	for _, frames := range [][]string{
		{"audio_reset", "speaking_interrupted", "turn_interrupted"},
		{"speaking_interrupted", "turn_interrupted", "audio_reset"},
	} {
		t.Run(frames[0], func(t *testing.T) {
			r := &frameReader{types: make(chan string, len(frames)+1)}
			for _, frame := range frames {
				r.types <- frame
			}
			// Leave the next operation's frame unread after all required frames.
			r.types <- "audio_output_ack"
			close(r.types)
			got := r.awaitAll(time.Second, "audio_reset", "speaking_interrupted", "turn_interrupted")
			if !reflect.DeepEqual(got, frames) {
				t.Fatalf("collected %v, want all interrupt frames %v", got, frames)
			}
			if next := <-r.types; next != "audio_output_ack" {
				t.Fatalf("consumed the next operation's frame: %q", next)
			}
		})
	}
}
