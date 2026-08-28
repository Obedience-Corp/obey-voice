package pipeline

import (
	"math"
	"math/rand"
)

const vocalSampleRate = 24000

// proceduralVocal renders a non-speech sound. These are not recordings and not
// TTS of the word "ahh" — they are breath + a falling voiced vowel, which is
// what Qwen3-TTS cannot emit. Drop real WAVs in the persona sounds dir to
// replace them with the cloned throat.
func proceduralVocal(kind string, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	switch kind {
	case vocalGasp:
		return synthGasp(rng)
	case vocalLaugh:
		return synthLaugh(rng)
	case vocalSigh:
		return synthSigh(rng)
	case vocalWhimper:
		return synthWhimper(rng)
	case vocalKiss:
		return synthKiss(rng)
	default:
		return synthMoan(rng)
	}
}

func synthMoan(rng *rand.Rand) []float32 {
	dur := 0.55 + rng.Float64()*0.45
	f0 := 195 + rng.Float64()*35
	return voicedBreath(rng, dur, f0, f0-40, 0.42, 0.22, []float64{700, 1200, 2450})
}

func synthWhimper(rng *rand.Rand) []float32 {
	dur := 0.28 + rng.Float64()*0.18
	f0 := 280 + rng.Float64()*40
	return voicedBreath(rng, dur, f0, f0-20, 0.32, 0.18, []float64{500, 1600, 2700})
}

func synthSigh(rng *rand.Rand) []float32 {
	dur := 0.7 + rng.Float64()*0.25
	f0 := 170 + rng.Float64()*25
	return voicedBreath(rng, dur, f0, f0-55, 0.22, 0.45, []float64{650, 1100, 2300})
}

func synthGasp(rng *rand.Rand) []float32 {
	n := int(vocalSampleRate * (0.16 + rng.Float64()*0.08))
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / vocalSampleRate
		env := attackRelease(t, float64(n)/vocalSampleRate, 0.02, 0.08)
		breath := (rng.Float64()*2 - 1) * 0.35
		// rising inhale
		f := 320 + t*400
		tone := math.Sin(2*math.Pi*f*t) * 0.08
		out[i] = float32((breath + tone) * env)
	}
	return softClip(out)
}

func synthLaugh(rng *rand.Rand) []float32 {
	pulses := 3 + rng.Intn(2)
	gap := 0.07
	pulse := 0.09
	n := int(vocalSampleRate * (float64(pulses)*(pulse+gap) + 0.05))
	out := make([]float32, n)
	f0 := 260 + rng.Float64()*40
	for p := 0; p < pulses; p++ {
		start := int(float64(p) * (pulse + gap) * vocalSampleRate)
		pn := int(pulse * vocalSampleRate)
		for i := 0; i < pn && start+i < n; i++ {
			t := float64(i) / vocalSampleRate
			env := attackRelease(t, pulse, 0.01, 0.04)
			h := math.Sin(2*math.Pi*f0*t) + 0.4*math.Sin(2*math.Pi*2*f0*t)
			breath := (rng.Float64()*2 - 1) * 0.12
			out[start+i] += float32((h*0.28 + breath) * env)
		}
		f0 -= 12
	}
	return softClip(out)
}

func synthKiss(rng *rand.Rand) []float32 {
	n := int(vocalSampleRate * 0.11)
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / vocalSampleRate
		env := attackRelease(t, 0.11, 0.004, 0.05)
		// wet click: decaying high noise + a tiny lip pop
		pop := 0.0
		if t < 0.012 {
			pop = math.Sin(2*math.Pi*900*t) * (1 - t/0.012) * 0.35
		}
		wet := (rng.Float64()*2 - 1) * 0.22 * math.Exp(-t*40)
		out[i] = float32((pop + wet) * env)
	}
	return softClip(out)
}

func voicedBreath(rng *rand.Rand, dur, f0, f1, voiceMix, breathMix float64, formants []float64) []float32 {
	n := int(dur * vocalSampleRate)
	out := make([]float32, n)
	var y1, y2 [3]float64
	phase := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / vocalSampleRate
		frac := t / dur
		env := attackRelease(t, dur, 0.06, 0.18)
		// unsteady amplitude — a held moan is never a flat oscillator
		env *= 0.85 + 0.15*math.Sin(2*math.Pi*(4.5+rng.Float64())*t)
		f := f0 + (f1-f0)*frac
		phase += 2 * math.Pi * f / vocalSampleRate
		glottal := math.Sin(phase) + 0.45*math.Sin(2*phase) + 0.18*math.Sin(3*phase)
		x := glottal * voiceMix
		for fi, formant := range formants {
			r := 0.92 - float64(fi)*0.04
			theta := 2 * math.Pi * formant / vocalSampleRate
			y := x + 2*r*math.Cos(theta)*y1[fi] - r*r*y2[fi]
			y2[fi], y1[fi] = y1[fi], y
			x = y
		}
		breath := (rng.Float64()*2 - 1) * breathMix * (0.5 + 0.5*frac)
		out[i] = float32((x + breath) * env * 0.55)
	}
	return softClip(out)
}

func attackRelease(t, dur, atk, rel float64) float64 {
	if dur <= 0 {
		return 0
	}
	if t < atk {
		return t / atk
	}
	if t > dur-rel && rel > 0 {
		return math.Max(0, (dur-t)/rel)
	}
	return 1
}

func softClip(in []float32) []float32 {
	for i, v := range in {
		x := float64(v)
		in[i] = float32(math.Tanh(x * 1.4))
	}
	return in
}
