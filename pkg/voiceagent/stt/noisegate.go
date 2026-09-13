package stt

import "math"

const (
	// gateHist is ~1s of 100ms capture frames. Speech amplitude moves on a
	// syllable timescale; a fan does not.
	gateHist = 10
	// gateSNR is ~8dB above the learned noise floor. A talker sitting on top
	// of a fan clears this; the fan itself does not.
	gateSNR = 2.5
	// gateCV is the coefficient of variation (stddev/mean) below which a
	// stretch of "speech" is treated as stationary noise. Real speech is
	// almost never this flat for a full history window.
	gateCV = 0.18
	// gateSilent is below a quiet room; digital zeros are not a fan latch.
	gateSilent = 1e-4
	// gateGainJump is an OS mixer slam (~18 dB), not a syllable. Syllables
	// sit around 2–4× the slow envelope; a volume slider often jumps 8×+.
	gateGainJump = 8.0
	// gateJumpFrames is how long the shifted level must hold before it counts
	// as a mixer move: 300ms of 100ms frames. One frame at 8× the envelope is
	// a syllable, a door, or a keyboard.
	gateJumpFrames = 3
)

// noiseGate is a second vote next to Silero. Neural VAD will happily latch on
// a laptop fan and never emit the silence that ends a turn. This tracks a
// noise floor and how much the level wobbles: Silero + modulation (or a jump
// above the floor) is speech; Silero + a flat hold at the floor is noise.
type noiseGate struct {
	hist    [gateHist]float64
	n       int
	i       int
	floor   float64
	env     float64 // slow RMS of non-speech frames only: the room, not the talker
	jumpDir int     // +1 louder, -1 quieter, 0 no run in progress
	jumpN   int     // consecutive non-speech frames off the envelope
	minRMS  float64 // not-silence floor for this path; 0 uses minSpeechRMS
}

type gateDecision struct {
	voice bool // count this frame as speech for endpointing
	drop  bool // Silero latched on stationary noise; reset the VAD
}

func (g *noiseGate) decide(samples []float32, silero bool) gateDecision {
	rms := frameRMS(samples)
	g.push(rms)

	// Only non-speech frames teach the gate about the room. A speech onset is
	// 8×+ above any quiet floor, so reading one as a mixer move dropped the
	// first frame of every utterance and reset the recognizer mid-sentence.
	if !silero {
		if g.reanchorOnGainJump(rms) {
			// Silero can still be accumulating its minimum speech duration.
			// Re-anchor the floor without deleting that pending onset or its
			// pre-roll: a false neural verdict has no speech latch to clear.
			return gateDecision{}
		}
		g.learn(rms, false)
		return gateDecision{}
	}

	// Silero on digital silence is a false mark. Never start a turn from
	// energy nobody could hear; the decoder will invent a word ("yeah").
	if rms < g.speechFloor() {
		return gateDecision{}
	}

	if g.floor > 0 && rms >= g.floor*gateSNR {
		return gateDecision{voice: true}
	}
	// Floor is learned only from Silero-false frames. A first held vowel is
	// stationary with floor==0; treating that window as noise ate the
	// utterance. Drop only a latch sitting on an already-learned floor.
	if g.stationary() && g.floor > 0 && rms < g.floor*gateSNR {
		return gateDecision{drop: true}
	}
	return gateDecision{voice: true}
}

// speechFloor is the lowest RMS this path counts as somebody making a sound.
func (g *noiseGate) speechFloor() float64 {
	if g.minRMS > 0 {
		return g.minRMS
	}
	return minSpeechRMS
}

// reanchorOnGainJump folds one non-speech frame into the mixer-move detector
// and reports whether the room level was re-anchored. A learned floor at the
// old gain treats hiss as speech (volume up) or drops real talkers (volume
// down), so the floor is re-seeded once the shifted level has held for
// gateJumpFrames. The envelope is frozen during a run so every frame in it is
// compared against the pre-move level.
//
// Because only non-speech frames vote, a Silero latch that never releases (a
// fan loud enough to hold the VAD through a volume change) is not recovered
// here; the stationarity check remains its only guard.
func (g *noiseGate) reanchorOnGainJump(rms float64) bool {
	if rms <= gateSilent {
		// A muted or disconnected mic is not a new room level to learn.
		g.jumpDir, g.jumpN = 0, 0
		return false
	}
	if g.env == 0 {
		g.env = rms
		return false
	}

	dir := 0
	switch {
	case rms > g.env*gateGainJump:
		dir = 1
	case rms < g.env/gateGainJump:
		dir = -1
	}
	if dir == 0 {
		g.jumpDir, g.jumpN = 0, 0
		g.env = 0.9*g.env + 0.1*rms
		return false
	}
	if dir != g.jumpDir {
		g.jumpDir, g.jumpN = dir, 0
	}
	g.jumpN++
	if g.jumpN < gateJumpFrames {
		return false
	}

	g.jumpDir, g.jumpN = 0, 0
	g.floor = rms
	g.env = rms
	return true
}

func (g *noiseGate) push(rms float64) {
	g.hist[g.i] = rms
	g.i = (g.i + 1) % gateHist
	if g.n < gateHist {
		g.n++
	}
}

func (g *noiseGate) learn(rms float64, latched bool) {
	if rms < gateSilent {
		return
	}
	if g.floor == 0 {
		g.floor = rms
		return
	}
	if latched || rms > g.floor {
		// Absorb a latched fan (or a slow rise in the room) without letting
		// one loud word become the new floor.
		g.floor = 0.7*g.floor + 0.3*rms
		return
	}
	g.floor = 0.6*g.floor + 0.4*rms
}

func (g *noiseGate) stationary() bool {
	if g.n < gateHist {
		return false
	}
	var sum, sq float64
	for i := 0; i < g.n; i++ {
		v := g.hist[i]
		sum += v
		sq += v * v
	}
	mean := sum / float64(g.n)
	if mean < gateSilent {
		return false
	}
	variance := sq/float64(g.n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)/mean < gateCV
}
