package scene

import (
	"fmt"
	"time"
)

// TurnPCM is one turn's audio at the rate the provider that spoke it produced.
// A turn whose line cleaned away to nothing is a legitimate zero-sample entry:
// silence is not a failure, and it takes no time on the combined timeline.
type TurnPCM struct {
	Samples []float32
	Rate    int
}

// TurnClock is where one turn lands in conversation.wav. These are the
// timestamps the video step reads: they are measured on the combined,
// post-resample timeline, not on the turn's own WAV, because a turn recorded at
// 16 kHz occupies a different number of samples once it is mixed at 24 kHz.
type TurnClock struct {
	StartMS    int64
	DurationMS int64
}

// Combined is the joined conversation plus the clock for every turn that went
// into it, in input order.
type Combined struct {
	Samples    []float32
	Rate       int
	PauseMS    int64
	DurationMS int64
	Clocks     []TurnClock
}

// ConcatTurns joins per-turn audio into one conversation, separated by pause.
//
// The combined rate is the first turn's native rate and everything else is
// converted to it. Clocks accumulate the integer millisecond values the
// manifest publishes — start[i+1] = start[i] + duration[i] + pause_ms — rather
// than being recomputed from sample offsets, so the identity a caption or
// avatar step relies on holds exactly in the units it reads. That identity
// covers turns that produced audio.
//
// A turn that produced none is still placed: it is stamped at the instant the
// previous turn ended and given a zero duration, so a silent or failed line
// keeps its position in the conversation instead of collapsing to the start.
// It spends no pause of its own, which is what "occupies no time" means here.
// There is no leading or trailing silence.
func ConcatTurns(turns []TurnPCM, pause time.Duration, resample Resampler) (Combined, error) {
	if resample == nil {
		return Combined{}, fmt.Errorf("scene: concat: no resampler")
	}
	if pause < 0 {
		return Combined{}, fmt.Errorf("scene: concat: pause %s must be >= 0", pause)
	}
	rate := combinedRate(turns)
	if rate <= 0 {
		return Combined{}, fmt.Errorf("scene: concat: no turn produced audio")
	}

	nPause := int(pause.Seconds() * float64(rate))
	out := Combined{
		Rate:    rate,
		PauseMS: samplesToMS(nPause, rate),
		Clocks:  make([]TurnClock, len(turns)),
	}
	var cursor int64
	spoken := false
	for i, turn := range turns {
		samples, err := turnSamples(turn, rate, resample)
		if err != nil {
			return Combined{}, fmt.Errorf("scene: concat: turn %d: %w", i, err)
		}
		if len(samples) == 0 {
			out.Clocks[i] = TurnClock{StartMS: cursor}
			continue
		}
		if spoken {
			out.Samples = append(out.Samples, make([]float32, nPause)...)
			cursor += out.PauseMS
		}
		duration := samplesToMS(len(samples), rate)
		out.Clocks[i] = TurnClock{StartMS: cursor, DurationMS: duration}
		out.Samples = append(out.Samples, samples...)
		cursor += duration
		spoken = true
	}
	if !spoken {
		return Combined{}, fmt.Errorf("scene: concat: no turn produced audio")
	}
	out.DurationMS = cursor
	return out, nil
}

// turnSamples returns one turn's audio at the combined rate.
func turnSamples(turn TurnPCM, rate int, resample Resampler) ([]float32, error) {
	if len(turn.Samples) == 0 || turn.Rate == rate {
		return turn.Samples, nil
	}
	return resample(turn.Samples, turn.Rate, rate)
}

// combinedRate is the first turn's native rate. Every later turn is converted
// to it, so a mixed Kokoro/Qwen cast records at whichever mouth speaks first
// instead of at a rate neither provider produced.
func combinedRate(turns []TurnPCM) int {
	for _, turn := range turns {
		if turn.Rate > 0 {
			return turn.Rate
		}
	}
	return 0
}

// samplesToMS is the locked duration formula: integer division, so the clocks
// the manifest publishes are exactly the numbers a consumer can add up.
func samplesToMS(samples, rate int) int64 {
	if rate <= 0 {
		return 0
	}
	return int64(samples) * 1000 / int64(rate)
}
