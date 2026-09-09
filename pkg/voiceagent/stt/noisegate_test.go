package stt

import (
	"fmt"
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

func TestNoiseGateAcceptsQuietRoomSpeech(t *testing.T) {
	t.Parallel()
	var g noiseGate
	fan := constFrame(1600, 0.0015)
	for i := 0; i < gateHist; i++ {
		g.decide(fan, false)
	}
	var voiced int
	off := 0
	for i := 0; i < gateHist; i++ {
		frame := modulatedFrame(1600, off, 0.004, 0.012)
		off += 1600
		if g.decide(frame, true).voice {
			voiced++
		}
	}
	if voiced < gateHist-2 {
		t.Fatalf("quiet room speech frames treated as voice = %d, want most of %d", voiced, gateHist)
	}
}

func TestNoiseGateReanchorsOnInputVolumeJump(t *testing.T) {
	t.Parallel()
	var g noiseGate
	quiet := constFrame(1600, 0.01)
	for i := 0; i < gateHist; i++ {
		g.decide(quiet, false)
	}
	if g.floor == 0 {
		t.Fatal("need a learned floor before the jump")
	}

	// 12x the learned level, held: a mixer, not a syllable. The gate learns
	// this from non-speech frames only, and only once the level has persisted.
	loud := constFrame(1600, 0.12)
	for i := 0; i < gateJumpFrames-1; i++ {
		if d := g.decide(loud, false); d.drop {
			t.Fatalf("frame %d: one loud frame is a door or a keyboard, not a volume change", i)
		}
	}
	d := g.decide(loud, false)
	if !d.drop {
		t.Fatalf("a level held %d frames above the room must re-anchor and drop the VAD latch", gateJumpFrames)
	}
	if g.floor < 0.1 {
		t.Fatalf("floor after re-anchor = %v, want the new room level", g.floor)
	}

	// After the re-anchor, a stationary hold at the new gain is still a fan.
	var sawDrop bool
	for i := 0; i < gateHist+2; i++ {
		d := g.decide(loud, true)
		if d.voice && i >= gateHist {
			t.Fatalf("frame %d: still treating post-jump hiss as speech", i)
		}
		if d.drop {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatal("after a gain jump, stationary noise must still drop the VAD latch")
	}
}

func TestNoiseGateReanchorsOnInputVolumeCut(t *testing.T) {
	t.Parallel()
	var g noiseGate
	loud := constFrame(1600, 0.08)
	for i := 0; i < gateHist; i++ {
		g.decide(loud, false)
	}

	// 20x down, held: the mixer moved. A floor learned at the old gain would
	// now reject every real talker in the room.
	quiet := constFrame(1600, 0.004)
	for i := 0; i < gateJumpFrames-1; i++ {
		if d := g.decide(quiet, false); d.drop {
			t.Fatalf("frame %d: one quiet frame is a pause, not a volume change", i)
		}
	}
	if d := g.decide(quiet, false); !d.drop {
		t.Fatalf("a cut held %d frames below the room must re-anchor the floor", gateJumpFrames)
	}
	if g.floor > 0.01 {
		t.Fatalf("floor after a volume cut = %v, want the new quiet room", g.floor)
	}

	off := 0
	for i := 0; i < 3; i++ {
		frame := modulatedFrame(1600, off, 0.01, 0.03)
		off += 1600
		d := g.decide(frame, true)
		if d.drop {
			t.Fatalf("frame %d dropped: speech at the new gain must survive the cut", i)
		}
		if !d.voice {
			t.Fatalf("frame %d: speech at the new gain must count as voice", i)
		}
	}
}

func TestNoiseGateVolumeJumpDoesNotDropASyllable(t *testing.T) {
	t.Parallel()
	// A speech onset is 8x+ above any room quieter than about 0.012, which is
	// every real room. Sweeping the floor is what caught the re-anchor firing
	// on ordinary speech: 0.02 was the only value that ever passed.
	for _, amp := range []float32{0.001, 0.002, 0.005, 0.008, 0.012, 0.02} {
		t.Run(fmt.Sprintf("floor_%g", amp), func(t *testing.T) {
			t.Parallel()
			var g noiseGate
			fan := constFrame(1600, amp)
			for i := 0; i < gateHist; i++ {
				g.decide(fan, false)
			}
			d := g.decide(modulatedFrame(1600, 0, 0.06, 0.16), true)
			if d.drop {
				t.Fatal("a normal syllable must not look like an OS volume jump")
			}
			if !d.voice {
				t.Fatal("a normal syllable must count as voice")
			}
		})
	}
}

func TestNoiseGateQuietRoomOnsetIsNotAVolumeJump(t *testing.T) {
	t.Parallel()
	var g noiseGate
	room := constFrame(1600, 0.001)
	for i := 0; i < gateHist; i++ {
		g.decide(room, false)
	}
	off := 0
	for i := 0; i < 5; i++ {
		frame := modulatedFrame(1600, off, 0.06, 0.16)
		off += 1600
		d := g.decide(frame, true)
		if d.drop {
			t.Fatalf("speech frame %d dropped: an onset after a quiet room is not a mixer move", i)
		}
		if !d.voice {
			t.Fatalf("speech frame %d: onset after a quiet room must count as voice", i)
		}
	}
}

func TestNoiseGateIntraUtterancePauseThenResumeIsNotDropped(t *testing.T) {
	t.Parallel()
	var g noiseGate
	room := constFrame(1600, 0.002)
	for i := 0; i < gateHist; i++ {
		g.decide(room, false)
	}
	off := 0
	for i := 0; i < 12; i++ {
		g.decide(modulatedFrame(1600, off, 0.06, 0.16), true)
		off += 1600
	}
	for i := 0; i < 2; i++ {
		if d := g.decide(room, false); d.drop {
			t.Fatalf("pause frame %d dropped: breathing mid-sentence is not a volume change", i)
		}
	}
	for i := 0; i < 3; i++ {
		frame := modulatedFrame(1600, off, 0.06, 0.16)
		off += 1600
		d := g.decide(frame, true)
		if d.drop {
			t.Fatalf("resume frame %d dropped: the streaming loop discards the whole in-flight utterance here", i)
		}
		if !d.voice {
			t.Fatalf("resume frame %d: the talker picking back up must count as voice", i)
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
		if d.voice {
			t.Fatalf("frame %d: digital silence must not count as a talker", i)
		}
	}
}
