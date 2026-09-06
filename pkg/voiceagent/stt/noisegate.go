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
)

// noiseGate is a second vote next to Silero. Neural VAD will happily latch on
// a laptop fan and never emit the silence that ends a turn. This tracks a
// noise floor and how much the level wobbles: Silero + modulation (or a jump
// above the floor) is speech; Silero + a flat hold at the floor is noise.
type noiseGate struct {
	hist  [gateHist]float64
	n     int
	i     int
	floor float64
}

type gateDecision struct {
	voice bool // count this frame as speech for endpointing
	drop  bool // Silero latched on stationary noise; reset the VAD
}

func (g *noiseGate) decide(samples []float32, silero bool) gateDecision {
	rms := frameRMS(samples)
	g.push(rms)

	if !silero {
		g.learn(rms, false)
		return gateDecision{}
	}

	// Silero on digital silence is a false mark. Never start a turn from
	// energy nobody could hear; the decoder will invent a word ("yeah").
	if rms < minSpeechRMS {
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
