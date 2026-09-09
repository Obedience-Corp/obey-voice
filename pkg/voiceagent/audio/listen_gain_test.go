package audio

import "testing"

func TestListenGainBoostsQuietSpeech(t *testing.T) {
	t.Parallel()
	g := NewListenGain()
	quiet := make([]float32, ChunkSize)
	for i := range quiet {
		quiet[i] = 0.004
	}
	var out []float32
	for range 12 {
		out = g.ProcessCapture(quiet)
	}
	got := frameRMS(out)
	if got <= frameRMS(quiet)*2 {
		t.Fatalf("rms after AGC = %v, want a boost of quiet room speech", got)
	}
	if got > 0.2 {
		t.Fatalf("rms after AGC = %v, want bounded (not an open-loop normalize)", got)
	}
}

func TestListenGainLeavesDigitalSilenceAtZero(t *testing.T) {
	t.Parallel()
	g := NewListenGain()
	z := make([]float32, ChunkSize)
	out := g.ProcessCapture(z)
	if frameRMS(out) != 0 {
		t.Fatalf("silence rms = %v, want 0", frameRMS(out))
	}
}

func TestListenGainDoesNotMuteCloseMicSpeech(t *testing.T) {
	t.Parallel()
	g := NewListenGain()
	closeMic := make([]float32, ChunkSize)
	for i := range closeMic {
		closeMic[i] = 0.08
	}
	var out []float32
	for range 8 {
		out = g.ProcessCapture(closeMic)
	}
	got := frameRMS(out)
	if got < 0.03 {
		t.Fatalf("close-mic rms = %v, AGC must not squash a desk talker", got)
	}
}
