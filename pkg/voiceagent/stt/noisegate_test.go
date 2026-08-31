package stt

import (
	"math"
	"testing"
)

func constFrame(n int, amp float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = amp
	}
	return out
}

func modulatedFrame(n int, t0 int, lo, hi float64) []float32 {
	out := make([]float32, n)
	span := hi - lo
	for i := range out {
		// ~4Hz syllable-rate AM at 16kHz.
		env := lo + span*0.5*(1+math.Sin(2*math.Pi*4*float64(t0+i)/16000))
		out[i] = float32(env * math.Sin(2*math.Pi*180*float64(t0+i)/16000))
	}
	return out
}

func TestNoiseGateRejectsStationaryNoiseEvenIfSileroTrue(t *testing.T) {
	var g noiseGate
	fan := constFrame(1600, 0.05)
	for i := 0; i < gateHist; i++ {
		g.decide(fan, false)
	}
	var sawDrop bool
	for i := 0; i < gateHist+2; i++ {
		d := g.decide(fan, true)
		if d.voice && i >= gateHist {
			t.Fatalf("frame %d: still treating a flat fan as speech", i)
		}
		if d.drop {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatal("stationary noise never asked the loop to drop the VAD latch")
	}
}

func TestNoiseGateTrustsModulatedSpeech(t *testing.T) {
	var g noiseGate
	// Learn a fan floor first.
	fan := constFrame(1600, 0.02)
	for i := 0; i < gateHist; i++ {
		g.decide(fan, false)
	}
	var voiced int
	off := 0
	for i := 0; i < gateHist; i++ {
		frame := modulatedFrame(1600, off, 0.08, 0.20)
		off += 1600
		if g.decide(frame, true).voice {
			voiced++
		}
	}
	if voiced < gateHist-2 {
		t.Fatalf("modulated speech frames treated as voice = %d, want most of %d", voiced, gateHist)
	}
}

func TestNoiseGateDoesNotDropFirstUtteranceWithoutAFloor(t *testing.T) {
	var g noiseGate
	off := 0
	for i := 0; i < gateHist+2; i++ {
		frame := modulatedFrame(1600, off, 0.06, 0.16)
		off += 1600
		d := g.decide(frame, true)
		if d.drop {
			t.Fatalf("frame %d: first utterance must not be dropped before a noise floor exists", i)
		}
		if !d.voice {
			t.Fatalf("frame %d: first utterance must count as voice", i)
		}
	}
}

func TestNoiseGateDoesNotDropConstantAmplitudeFirstUtterance(t *testing.T) {
	var g noiseGate
	vowel := constFrame(1600, 0.08)
	for i := 0; i < gateHist+2; i++ {
		d := g.decide(vowel, true)
		if d.drop {
			t.Fatalf("frame %d: constant-amplitude first utterance must not be dropped", i)
		}
		if !d.voice {
			t.Fatalf("frame %d: constant-amplitude first utterance must count as voice", i)
		}
	}
}

func TestNoiseGateSilentZerosAreNotAFanLatch(t *testing.T) {
	var g noiseGate
	z := constFrame(1600, 0)
	for i := 0; i < gateHist+2; i++ {
		d := g.decide(z, true)
		if d.drop {
			t.Fatalf("frame %d: digital silence must not look like latched noise", i)
		}
	}
}
