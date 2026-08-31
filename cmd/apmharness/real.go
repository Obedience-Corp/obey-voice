//go:build apm

package main

import (
	"fmt"
	"strconv"

	"github.com/Obedience-Corp/obey-voice/internal/aecprobe"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/apm"
)

// runReal streams two real recordings (a playback reference and what the
// microphone heard) through the shim and reports ERLE, the same way
// internal/aecprobe scores a hardware run — mic-in against mic-out, so
// whatever the library's own noise/gain stages do is honestly included.
func runReal(o options) error {
	render, renderRate, err := audio.ReadWAVFloat32(o.render)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.render, err)
	}
	capture, captureRate, err := audio.ReadWAVFloat32(o.capture)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.capture, err)
	}
	if renderRate != aecprobe.Rate || captureRate != aecprobe.Rate {
		return fmt.Errorf("apmharness requires 16kHz mono S16 WAVs; got render=%dHz capture=%dHz", renderRate, captureRate)
	}

	delayMs, confidence, delaySource := resolveRealDelay(o, render, capture)
	fmt.Printf("render: %s (%.2fs, RMS %.4f)\n", o.render, seconds(len(render), aecprobe.Rate), aecprobe.SegmentRMS(render))
	fmt.Printf("capture: %s (%.2fs, RMS %.4f, peak %.4f)\n", o.capture, seconds(len(capture), aecprobe.Rate), aecprobe.SegmentRMS(capture), aecprobe.PeakLevel(capture))
	fmt.Printf("delay: %dms (%s", delayMs, delaySource)
	if delaySource == "measured" {
		fmt.Printf(", confidence %.2f", confidence)
	}
	fmt.Println(")")

	a, err := apm.New(aecprobe.Rate, o.enableNS, apm.NSLevel(o.nsLevel), false)
	if err != nil {
		return fmt.Errorf("apm.New: %w", err)
	}
	defer a.Close()
	if err := a.SetStreamDelayMs(delayMs); err != nil {
		return fmt.Errorf("SetStreamDelayMs: %w", err)
	}

	frameLen := a.FrameSamples()
	n := min(len(render), len(capture))
	n -= n % frameLen
	if n <= 0 {
		return fmt.Errorf("recordings are shorter than one %dms frame", frameLen*1000/aecprobe.Rate)
	}
	if len(render) != len(capture) {
		fmt.Printf("warning: render is %.2fs, capture is %.2fs — scoring the shared %.2fs\n",
			seconds(len(render), aecprobe.Rate), seconds(len(capture), aecprobe.Rate), seconds(n, aecprobe.Rate))
	}

	micOut := make([]float32, n)
	for start := 0; start < n; start += frameLen {
		renderFrame := toInt16(render[start : start+frameLen])
		captureFrame := toInt16(capture[start : start+frameLen])
		if err := a.ProcessRender(renderFrame); err != nil {
			return fmt.Errorf("ProcessRender at %.2fs: %w", seconds(start, aecprobe.Rate), err)
		}
		outFrame, err := a.ProcessCapture(captureFrame)
		if err != nil {
			return fmt.Errorf("ProcessCapture at %.2fs: %w", seconds(start, aecprobe.Rate), err)
		}
		copy(micOut[start:], fromInt16(outFrame))
	}

	erle := aecprobe.ERLE(capture[:n], micOut)
	stats := a.Stats()
	fmt.Printf("\nERLE (mic-in vs mic-out): %+.2f dB\n", erle)
	printStats(stats)

	if o.out != "" {
		if err := audio.WriteWAVFloat32(o.out, aecprobe.Rate, micOut); err != nil {
			return fmt.Errorf("write %s: %w", o.out, err)
		}
		fmt.Printf("wrote %s\n", o.out)
	}
	return nil
}

// resolveRealDelay returns the render->capture delay to hand APM. -delay-ms
// overrides auto-measurement when it parses as a single integer; otherwise
// the delay is measured the same way the hardware probe does, via
// aecprobe.MeasureDelay (audio.CorrelateDelay under the hood), so a harness
// run and a probe run of the same pair never disagree about the number.
func resolveRealDelay(o options, render, capture []float32) (delayMs int, confidence float64, source string) {
	if ms, err := strconv.Atoi(o.delayMs); err == nil {
		return ms, 0, "flag"
	}
	maxLag := o.maxLagMs * aecprobe.Rate / 1000
	lag, conf := aecprobe.MeasureDelay(render, capture, maxLag)
	if conf < 0.3 {
		fmt.Printf("warning: delay correlation confidence %.2f is weak; consider passing -delay-ms explicitly\n", conf)
	}
	return lag * 1000 / aecprobe.Rate, conf, "measured"
}

func seconds(samples, rate int) float64 { return float64(samples) / float64(rate) }

func printStats(s apm.Stats) {
	fmt.Println("\nAPM-reported stats (may lag the just-processed audio by up to ~1s; the library aggregates internally):")
	printOptFloat("  echo return loss (ERL)", s.EchoReturnLoss, s.HasEchoReturnLoss, "dB")
	printOptFloat("  echo return loss enhancement (ERLE)", s.EchoReturnLossEnhancement, s.HasEchoReturnLossEnhancement, "dB")
	printOptFloat("  divergent filter fraction", s.DivergentFilterFraction, s.HasDivergentFilterFraction, "")
	printOptInt("  delay median", int(s.DelayMedianMs), s.HasDelayMedianMs, "ms")
	printOptInt("  delay stddev", int(s.DelayStandardDeviationMs), s.HasDelayStandardDeviationMs, "ms")
	printOptInt("  instantaneous delay", int(s.DelayMs), s.HasDelayMs, "ms")
	printOptFloat("  residual echo likelihood", s.ResidualEchoLikelihood, s.HasResidualEchoLikelihood, "")
	printOptFloat("  residual echo likelihood (recent max)", s.ResidualEchoLikelihoodRecentMax, s.HasResidualEchoLikelihoodRecentMax, "")
	if s.HasVoiceDetected {
		fmt.Printf("  voice detected: %v\n", s.VoiceDetected)
	}
	printOptInt("  output level", int(s.OutputRMSDBFS), s.HasOutputRMSDBFS, "dBFS")
}

func printOptFloat(label string, v float64, has bool, unit string) {
	if !has {
		return
	}
	if unit != "" {
		fmt.Printf("%s: %.3f %s\n", label, v, unit)
		return
	}
	fmt.Printf("%s: %.3f\n", label, v)
}

func printOptInt(label string, v int, has bool, unit string) {
	if !has {
		return
	}
	fmt.Printf("%s: %d %s\n", label, v, unit)
}
