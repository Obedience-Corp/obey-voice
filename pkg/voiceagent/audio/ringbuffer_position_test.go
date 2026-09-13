package audio

import (
	"slices"
	"testing"
)

func TestRingBufferPositionsAcrossReadsAndWrap(t *testing.T) {
	rb := NewRingBuffer(5)
	if got := rb.WriteWithPosition([]float32{1, 2, 3}); got != 0 {
		t.Fatalf("first position=%d", got)
	}
	if got, pos := rb.ReadWithPosition(2); !slices.Equal(got, []float32{1, 2}) || pos != 0 {
		t.Fatalf("split read=%v pos=%d", got, pos)
	}
	if got := rb.WriteWithPosition([]float32{4, 5, 6, 7}); got != 3 {
		t.Fatalf("second position=%d", got)
	}
	got, pos := rb.ReadWithPosition(5)
	if !slices.Equal(got, []float32{3, 4, 5, 6, 7}) || pos != 2 {
		t.Fatalf("wrapped read=%v pos=%d", got, pos)
	}
}

func TestRingBufferPositionsOverwriteClearKeepAndLegacy(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.Write([]float32{10, 11, 12, 13, 14})
	got, pos := rb.ReadWithPosition(4)
	if !slices.Equal(got, []float32{11, 12, 13, 14}) || pos != 1 {
		t.Fatalf("overwrite=%v pos=%d", got, pos)
	}
	rb.WriteWithPosition([]float32{20, 21, 22, 23})
	rb.KeepFrom(7)
	got, pos = rb.ReadWithPosition(2)
	if !slices.Equal(got, []float32{22, 23}) || pos != 7 {
		t.Fatalf("keep from=%v pos=%d", got, pos)
	}
	rb.WriteWithPosition([]float32{30, 31})
	rb.KeepLast(1)
	got, pos = rb.ReadWithPosition(1)
	if !slices.Equal(got, []float32{31}) || pos != 10 {
		t.Fatalf("keep last=%v pos=%d", got, pos)
	}
	rb.WriteWithPosition([]float32{40})
	rb.Clear()
	if rb.Available() != 0 {
		t.Fatal("clear retained samples")
	}
	if got := rb.WriteWithPosition([]float32{50}); got != 12 {
		t.Fatalf("clear reset absolute position=%d", got)
	}
	if got := rb.Read(1); !slices.Equal(got, []float32{50}) {
		t.Fatalf("legacy read=%v", got)
	}
}

func TestRingBufferKeepFromCannotReplayAndClampsFutureOffsets(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.WriteWithPosition([]float32{1, 2, 3})
	if got := rb.Read(1); !slices.Equal(got, []float32{1}) {
		t.Fatalf("initial read=%v", got)
	}
	rb.KeepFrom(0)
	got, pos := rb.ReadWithPosition(2)
	if !slices.Equal(got, []float32{2, 3}) || pos != 1 {
		t.Fatalf("stale keep=%v pos=%d", got, pos)
	}
	rb.KeepFrom(99)
	if rb.Available() != 0 {
		t.Fatal("future keep retained old samples")
	}
	if got := rb.WriteWithPosition([]float32{4, 5}); got != 3 {
		t.Fatalf("future keep reset position=%d", got)
	}
	got, pos = rb.ReadWithPosition(2)
	if !slices.Equal(got, []float32{4, 5}) || pos != 3 {
		t.Fatalf("later write=%v pos=%d", got, pos)
	}
}
