package audio

import (
	"context"
	"testing"
	"time"
)

func TestPCMStreamRecordsFirstWriteAndCloseObservations(t *testing.T) {
	stream := NewPCMStream(context.Background())
	if err := stream.SetSampleRate(16000); err != nil {
		t.Fatal(err)
	}
	if err := stream.Write([]float32{0.1, 0.2}); err != nil {
		t.Fatal(err)
	}
	first := stream.FirstWrittenAt()
	if first.IsZero() {
		t.Fatal("first PCM write was not observed")
	}
	if !stream.ClosedAt().IsZero() {
		t.Fatal("stream reported closed before Close")
	}
	stream.Close()
	closed := stream.ClosedAt()
	if closed.IsZero() {
		t.Fatal("stream close was not observed")
	}
	if closed.Before(first) {
		t.Fatalf("close %v precedes first write %v", closed, first)
	}
	if stream.FirstWrittenAt().After(time.Now()) {
		t.Fatal("first write timestamp is in the future")
	}
}
