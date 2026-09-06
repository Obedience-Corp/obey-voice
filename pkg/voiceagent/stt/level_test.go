package stt

import (
	"testing"
	"time"
)

func TestAudibleSpeechRejectsSilenceAndAcceptsSpeech(t *testing.T) {
	t.Parallel()
	if audibleSpeech(nil) || audibleSpeech(make([]float32, minSpeechSamples)) {
		t.Fatal("silence must not count as a talker")
	}
	if !audibleSpeech(constFrame(minSpeechSamples, 0.08)) {
		t.Fatal("close-mic speech must count as a talker")
	}
	mixed := append(make([]float32, 4800), constFrame(1600, 0.08)...)
	if !audibleSpeech(mixed) {
		t.Fatal("speech after silent pre-roll must still count")
	}
}

func TestFrameRMSAndNormalize(t *testing.T) {
	if frameRMS(nil) != 0 {
		t.Fatal("empty RMS must be 0")
	}
	silent := make([]float32, 100)
	if frameRMS(silent) != 0 {
		t.Fatal("silence RMS must be 0")
	}
	loud := make([]float32, 100)
	for i := range loud {
		loud[i] = 0.2
	}
	rms := frameRMS(loud)
	if rms < 0.19 || rms > 0.21 {
		t.Fatalf("rms = %v, want ~0.2", rms)
	}
	level := normalizeInputLevel(rms)
	if level != 1 {
		t.Fatalf("normalize(0.2) = %v, want 1 (clamped)", level)
	}
	soft := normalizeInputLevel(0.06)
	if soft <= 0 || soft >= 1 {
		t.Fatalf("normalize(0.06) = %v, want mid range", soft)
	}
}

func TestLevelEmitterThrottles(t *testing.T) {
	ch := make(chan Event, 8)
	var e levelEmitter
	samples := make([]float32, 64)
	for i := range samples {
		samples[i] = 0.1
	}
	e.maybeEmit(ch, samples)
	e.maybeEmit(ch, samples) // same window — should drop
	if len(ch) != 1 {
		t.Fatalf("emits = %d, want 1 after throttle", len(ch))
	}
	// Force next window.
	e.last = time.Now().Add(-levelEmitMinInterval * 2)
	e.maybeEmit(ch, samples)
	if len(ch) != 2 {
		t.Fatalf("emits = %d, want 2 after interval", len(ch))
	}
	ev := <-ch
	lvl, ok := ev.(InputLevel)
	if !ok || lvl.Level <= 0 {
		t.Fatalf("event = %#v, want InputLevel > 0", ev)
	}
}

func TestTrySendEventDropsWhenFull(t *testing.T) {
	ch := make(chan Event) // unbuffered
	// Non-blocking send into full/unbuffered channel without receiver.
	trySendEvent(ch, InputLevel{Level: 0.5})
	// If we get here without blocking, the drop path works.
}

func TestAudibleTrackerVerdictIsChunkInvariant(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stream []float32
		want   bool
	}{
		{"silence", make([]float32, 8000), false},
		{"speech", constFrame(8000, 0.08), true},
		{"speech after long pre-roll", append(make([]float32, 6400), constFrame(1600, 0.08)...), true},
		// Straddles a window boundary: only the trailing window sees it.
		{"short word at the end", append(make([]float32, 2000), constFrame(400, 0.08)...), true},
	}
	for _, tc := range cases {
		for _, chunk := range []int{1, 160, 1600, 4096} {
			var tr audibleTracker
			for i := 0; i < len(tc.stream); i += chunk {
				end := i + chunk
				if end > len(tc.stream) {
					end = len(tc.stream)
				}
				tr.push(tc.stream[i:end])
			}
			if got := tr.audible(); got != tc.want {
				t.Fatalf("%s in %d-sample chunks: audible = %v, want %v", tc.name, chunk, got, tc.want)
			}
		}
	}
}

func TestAudibleTrackerStaysBoundedOverALongStream(t *testing.T) {
	silent := make([]float32, audibleWindow)

	var tr audibleTracker
	if allocs := testing.AllocsPerRun(200, func() { tr.push(silent) }); allocs != 0 {
		t.Fatalf("push allocs = %v, want 0 (a live stream must not accumulate audio)", allocs)
	}
	for i := 0; i < 6000; i++ { // 10 minutes of capture
		tr.push(silent)
	}
	if tr.audible() {
		t.Fatal("ten minutes of silence must not count as a talker")
	}

	// The audio is discarded, the verdict is not: speech before a long silence
	// still finalizes as a turn.
	var early audibleTracker
	early.push(constFrame(audibleWindow, 0.08))
	for i := 0; i < 6000; i++ {
		early.push(silent)
	}
	if !early.audible() {
		t.Fatal("speech before a long silence must stay audible")
	}
}

func TestAudibleTrackerResetClearsTheVerdict(t *testing.T) {
	t.Parallel()
	var tr audibleTracker
	tr.push(constFrame(audibleWindow, 0.08))
	tr.reset()
	tr.push(make([]float32, audibleWindow))
	if tr.audible() {
		t.Fatal("reset must drop the previous utterance's verdict")
	}
}
