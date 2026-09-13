package audio

import "testing"

func TestRingBufferKeepLastRetainsNewestUnreadSamples(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]float32{1, 2, 3, 4, 5, 6})
	rb.KeepLast(3)

	got := rb.Read(3)
	want := []float32{4, 5, 6}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRingBufferKeepLastWorksAfterWrap(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.Write([]float32{1, 2, 3, 4})
	if got := rb.Read(2); len(got) != 2 {
		t.Fatalf("initial read length = %d, want 2", len(got))
	}
	rb.Write([]float32{5, 6, 7})
	rb.KeepLast(2)

	got := rb.Read(2)
	want := []float32{6, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}
