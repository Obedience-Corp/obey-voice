// Command apmharness measures the webrtc-audio-processing (AEC3) cgo shim in
// pkg/voiceagent/audio/apm, offline — no microphone or speaker required.
//
// Two modes:
//
//   - Real recordings: -render and -capture point at 16kHz mono S16 WAV
//     files (a playback reference and what the microphone heard), streamed
//     through the shim in 10ms frames. Reports the delay APM was given and
//     the resulting ERLE, reusing internal/aecprobe's delay correlation and
//     ERLE math so this and the hardware probe never disagree about what
//     either number means.
//
//   - -selftest: synthesizes a reference signal, convolves a fake echo path
//     at a chosen delay, adds independent near-end speech starting halfway
//     through, and reports both the achieved ERLE and how much of the
//     near-end signal survived — mirroring the methodology in
//     pkg/voiceagent/audio/voice_frontend_erle_test.go, so the two AEC
//     candidates (the shipped NLMS and this shim) are scored the same way.
//
// Usage:
//
//	apmharness -selftest -delay-ms 8,16,32,64,100
//	apmharness -render ref.wav -capture mic.wav -out mic-out.wav
//
//go:build apm

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apmharness:", err)
		os.Exit(1)
	}
}

type options struct {
	render  string
	capture string
	out     string

	selftest    bool
	rate        int
	delayMs     string
	delayHintMs int
	maxLagMs    int
	seconds     float64

	enableNS bool
	nsLevel  int
	verbose  bool
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.render, "render", "", "reference/playback WAV, 16kHz mono S16 (real mode)")
	flag.StringVar(&o.capture, "capture", "", "microphone WAV, 16kHz mono S16 (real mode)")
	flag.StringVar(&o.out, "out", "", "write the processed capture to this WAV (optional, either mode)")

	flag.BoolVar(&o.selftest, "selftest", false, "synthesize render/capture instead of reading WAVs")
	flag.IntVar(&o.rate, "rate", 16000, "selftest: sample rate — one of 8000, 16000, 32000, 48000 (a webrtc AudioProcessing NativeRate)")
	flag.StringVar(&o.delayMs, "delay-ms", "16", "selftest: comma-separated echo-path delays to sweep, e.g. 8,16,32,64,100. real mode: overrides the auto-measured render->capture delay when set to a single value")
	flag.IntVar(&o.delayHintMs, "delay-hint-ms", -1, "selftest: what to tell SetStreamDelayMs, if different from the true synthesized delay (-1 = use the true delay)")
	flag.IntVar(&o.maxLagMs, "max-lag-ms", 1000, "real mode: maximum lag searched when auto-measuring the render->capture delay")
	flag.Float64Var(&o.seconds, "seconds", 8, "selftest: duration of the synthesized signal")

	flag.BoolVar(&o.enableNS, "ns", false, "enable the noise suppressor alongside AEC3")
	flag.IntVar(&o.nsLevel, "ns-level", 1, "noise suppressor level: 0=low 1=moderate 2=high 3=very high")
	flag.BoolVar(&o.verbose, "verbose", false, "print every Stats() field, not just ERLE")
	flag.Parse()
	return o
}

func run() error {
	o := parseFlags()

	if o.selftest {
		return runSelftest(o)
	}
	if o.render == "" || o.capture == "" {
		flag.Usage()
		return fmt.Errorf("-render and -capture are required (or pass -selftest)")
	}
	return runReal(o)
}
