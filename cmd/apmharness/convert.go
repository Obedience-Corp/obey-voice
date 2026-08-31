//go:build apm

package main

import "math"

// toInt16 converts [-1,1] float32 samples to the int16 PCM frames the apm
// shim requires, clamping rather than wrapping on overshoot.
func toInt16(samples []float32) []int16 {
	out := make([]int16, len(samples))
	for i, s := range samples {
		v := math.Round(float64(s) * math.MaxInt16)
		switch {
		case v > math.MaxInt16:
			v = math.MaxInt16
		case v < math.MinInt16:
			v = math.MinInt16
		}
		out[i] = int16(v)
	}
	return out
}

// fromInt16 converts int16 PCM back to [-1,1] float32, the domain
// internal/aecprobe's ERLE and delay-correlation math operates in.
func fromInt16(samples []int16) []float32 {
	out := make([]float32, len(samples))
	for i, s := range samples {
		out[i] = float32(s) / math.MaxInt16
	}
	return out
}
