//go:build apm

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/aecprobe"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apm"
)

// selftestEchoGain matches erleEchoGain in voice_frontend_erle_test.go, so a
// selftest run and the shipped NLMS's own unit tests describe the same
// acoustic scenario.
const selftestEchoGain = 0.5

// runSelftest synthesizes a render signal and a fake echo path at each
// requested delay, adds independent near-end speech starting halfway
// through, and reports the ERLE achieved on the echo-only half plus how much
// of the near-end signal survived once double-talk starts — the same two
// questions pkg/voiceagent/audio/voice_frontend_erle_test.go asks of the
// shipped NLMS canceller.
func runSelftest(o options) error {
	delays, err := parseDelayList(o.delayMs)
	if err != nil {
		return err
	}

	fmt.Printf("%-8s %-8s %10s %14s %16s %14s\n",
		"delay", "hint", "ERLE(dB)", "near-end(dB)", "APM ERLE@half", "APM ERLE@end")
	for _, delayMs := range delays {
		hintMs := o.delayHintMs
		if hintMs < 0 {
			hintMs = delayMs
		}
		result, err := selftestOne(o, delayMs, hintMs)
		if err != nil {
			return fmt.Errorf("delay=%dms: %w", delayMs, err)
		}
		fmt.Printf("%-8s %-8s %+10.2f %+14.2f %16s %14s\n",
			fmt.Sprintf("%dms", delayMs), fmt.Sprintf("%dms", hintMs),
			result.erle, result.nearEndPreservedDB,
			optionalERLE(result.statsEchoOnly), optionalERLE(result.statsFinal))
		if o.verbose {
			fmt.Printf("  -- stats at end of run (delay=%dms) --\n", delayMs)
			printStats(result.statsFinal)
		}
	}
	return nil
}

func optionalERLE(s apm.Stats) string {
	if !s.HasEchoReturnLossEnhancement {
		return "n/a"
	}
	return fmt.Sprintf("%+.2f", s.EchoReturnLossEnhancement)
}

func parseDelayList(spec string) ([]int, error) {
	var out []int
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		ms, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("-delay-ms %q: %w", spec, err)
		}
		out = append(out, ms)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-delay-ms did not name any delays")
	}
	return out, nil
}

type selftestResult struct {
	erle               float64
	nearEndPreservedDB float64
	// statsEchoOnly is read the instant the echo-only phase ends, before
	// double-talk starts; statsFinal is read at the very end. GetStatistics()
	// aggregates from process start on its first call and roughly every
	// second after, so a single end-of-run read blends the clean echo-only
	// half with the double-talk half — statsEchoOnly exists to tell whether
	// that blending, not the canceller, explains a surprising APM-reported
	// number.
	statsEchoOnly apm.Stats
	statsFinal    apm.Stats
}

func selftestOne(o options, delayMs, hintMs int) (selftestResult, error) {
	rate := o.rate
	total := int(o.seconds * float64(rate))
	delaySamples := delayMs * rate / 1000

	// Deterministic seeds: far-end and near-end must differ so near-end
	// preservation is measuring something real, not correlated leakage.
	far := aecprobe.SpeechLikeNoise(o.seconds, rate, 1)
	near := aecprobe.SpeechLikeNoise(o.seconds, rate, 99)

	mic := make([]float32, total)
	for i := range mic {
		if src := i - delaySamples; src >= 0 {
			mic[i] = selftestEchoGain * far[src]
		}
		// The user starts talking halfway in, over the still-playing echo —
		// the barge-in moment TestEchoCancellerPreservesNearEndDuringDoubleTalk
		// exercises for the NLMS canceller.
		if i >= total/2 {
			mic[i] += near[i]
		}
	}

	a, err := apm.New(rate, o.enableNS, apm.NSLevel(o.nsLevel))
	if err != nil {
		return selftestResult{}, fmt.Errorf("apm.New: %w", err)
	}
	defer a.Close()
	if err := a.SetStreamDelayMs(hintMs); err != nil {
		return selftestResult{}, fmt.Errorf("SetStreamDelayMs: %w", err)
	}

	frameLen := a.FrameSamples()
	micOut := make([]float32, total)
	var statsEchoOnly apm.Stats
	tookEchoOnlySnapshot := false
	for start := 0; start+frameLen <= total; start += frameLen {
		if err := a.ProcessRender(toInt16(far[start : start+frameLen])); err != nil {
			return selftestResult{}, fmt.Errorf("ProcessRender at %.2fs: %w", seconds(start, rate), err)
		}
		outFrame, err := a.ProcessCapture(toInt16(mic[start : start+frameLen]))
		if err != nil {
			return selftestResult{}, fmt.Errorf("ProcessCapture at %.2fs: %w", seconds(start, rate), err)
		}
		copy(micOut[start:], fromInt16(outFrame))

		if !tookEchoOnlySnapshot && start+frameLen >= total/2 {
			statsEchoOnly = a.Stats()
			tookEchoOnlySnapshot = true
		}
	}

	// Echo-only window: after the filter has had a quarter of the run to
	// adapt, before near-end speech starts at the halfway point.
	warmupStart, warmupEnd := total/4, total/2
	erle := aecprobe.ERLE(mic[warmupStart:warmupEnd], micOut[warmupStart:warmupEnd])

	// Near-end preservation window: double-talk well established, mirroring
	// the NLMS test's own scoring window (start*3/4 onward).
	var outEnergy, nearEnergy float64
	for i := total * 3 / 4; i < total; i++ {
		outEnergy += float64(micOut[i]) * float64(micOut[i])
		nearEnergy += float64(near[i]) * float64(near[i])
	}
	nearEndPreservedDB := 10 * math.Log10(outEnergy/math.Max(nearEnergy, 1e-12))

	return selftestResult{
		erle:               erle,
		nearEndPreservedDB: nearEndPreservedDB,
		statsEchoOnly:      statsEchoOnly,
		statsFinal:         a.Stats(),
	}, nil
}
