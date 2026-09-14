package stt

import (
	"math"
	"slices"
	"testing"
)

func audibilitySamples(n int, level float32) []float32 {
	samples := make([]float32, n)
	for i := range samples {
		// Alternating signs avoid relying on a DC signal being speech. This
		// check measures audibility; neural speech detection is a separate vote.
		samples[i] = level
		if i%2 != 0 {
			samples[i] = -level
		}
	}
	return samples
}

func TestSpeechAudibilitySeparatesAmplifiedEchoAndVoice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level float32
		want  bool
	}{
		{"quiet echo amplified sixfold", 0.001 * 6, false},
		{"room voice amplified sixfold", 0.003 * 6, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var admission SpeechAudibility
			if admission.Audible() {
				t.Fatal("empty utterance is audible")
			}
			got := admission.Observe(audibilitySamples(audibleWindow, tc.level), 6)
			if got != tc.want || admission.Audible() != tc.want {
				t.Fatalf("audible = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestSpeechAudibilityNormalizesEachCallbacksGain(t *testing.T) {
	var quiet SpeechAudibility
	if quiet.Observe(audibilitySamples(audibleWindow/2, 0.0015*6), 6) {
		t.Fatal("amplified room tone admitted")
	}
	if quiet.Observe(audibilitySamples(audibleWindow/2, 0.0015), 1) {
		t.Fatal("lowering gain reinterpreted earlier room tone as audible")
	}

	var voice SpeechAudibility
	voice.Observe(audibilitySamples(audibleWindow/2, 0.003), 1)
	if !voice.Observe(audibilitySamples(audibleWindow/2, 0.003*6), 6) {
		t.Fatal("raising gain discarded the earlier part of an audible word")
	}
	if !voice.Observe(make([]float32, audibleWindow*2), 1) {
		t.Fatal("quiet callbacks diluted a completed audible window")
	}
}

func TestSpeechAudibilityPreservesTrailingAndLatchedVerdicts(t *testing.T) {
	var admission SpeechAudibility
	// A short trailing fragment is provisionally audible, as in audibleTracker.
	if !admission.Observe(audibilitySamples(100, 0.003*6), 6) {
		t.Fatal("audible trailing fragment was lost")
	}
	if admission.Observe(make([]float32, audibleWindow-100), 6) {
		t.Fatal("a provisional tail was incorrectly latched across silence")
	}
	if !admission.Observe(audibilitySamples(audibleWindow, 0.003*6), 6) {
		t.Fatal("completed audible window was lost")
	}
	if !admission.Observe(make([]float32, audibleWindow*3), 6) {
		t.Fatal("completed audible window did not remain latched")
	}
	admission.Reset()
	if admission.Audible() || admission.Observe(make([]float32, audibleWindow), 6) {
		t.Fatal("reset retained previous utterance audibility")
	}
}

func TestSpeechAudibilityMatchesSpeechFloorForNonAmplifyingGain(t *testing.T) {
	for _, gain := range []float64{0, 0.5, 1, math.NaN()} {
		var admission SpeechAudibility
		if admission.Observe(audibilitySamples(audibleWindow, 0.0015), gain) {
			t.Fatalf("gain %v amplified a below-floor signal", gain)
		}
		if !admission.Observe(audibilitySamples(audibleWindow, 0.003), gain) {
			t.Fatalf("gain %v rejected an above-floor signal", gain)
		}
	}
}

func TestSpeechAudibilityPreservesInputWithoutAllocating(t *testing.T) {
	// Cross multiple scratch blocks while staying below the floor, so the
	// tracker must examine the entire callback rather than immediately latch.
	samples := audibilitySamples(audibleWindow*2+17, 0.001*6)
	original := slices.Clone(samples)
	var admission SpeechAudibility
	allocs := testing.AllocsPerRun(100, func() {
		admission.Reset()
		admission.Observe(samples, 6)
	})
	if allocs != 0 {
		t.Fatalf("Observe allocated %.1f times per callback", allocs)
	}
	if !slices.Equal(samples, original) {
		t.Fatal("Observe changed the capture samples")
	}
}
