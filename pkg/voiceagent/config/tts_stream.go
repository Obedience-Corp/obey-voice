package config

import (
	"fmt"
	"strings"
)

// How this host consumes a Stage B worker (qwen_tts_stream). Streaming changes
// when the first sample arrives, not how fast samples are produced, so it is
// only safe where synthesis already runs faster than realtime. Measured
// 2026-08-27 (qwen3-tts-native docs/latency/stage_b_streaming_2026-08-27.json):
// CUDA passes the gate with roughly 2x headroom (RTF 0.55, 0 underruns over 15
// runs), Metal fails it outright (RTF 1.66 — worse than its own 1.40
// whole-utterance baseline — and 120 underruns over the same 15 runs).
const (
	// QwenStreamAuto plays chunks only where they were measured not to
	// underrun. See QwenStreamEligible.
	QwenStreamAuto = "auto"
	// QwenStreamOn asks any capable worker to stream, whatever the place.
	QwenStreamOn = "on"
	// QwenStreamOff pins whole-utterance Stage A even on a capable worker.
	QwenStreamOff = "off"
)

// QwenBackendCUDA is the install manifest's backend_hint for a package built
// against CUDA. It is the only local backend measured to pass the gate.
const QwenBackendCUDA = "cuda"

func qwenStreamEnum() []string { return []string{QwenStreamAuto, QwenStreamOn, QwenStreamOff} }

// QwenTTSStreamOf normalizes cfg's streaming mode. Empty is auto, so an install
// that predates the key gets the measured-safe policy rather than an error.
func QwenTTSStreamOf(cfg *Config) string {
	if cfg == nil {
		return QwenStreamAuto
	}
	switch mode := strings.ToLower(strings.TrimSpace(cfg.QwenTTSStream)); mode {
	case "":
		return QwenStreamAuto
	default:
		return mode
	}
}

// ValidateQwenTTSStream rejects a mode this host cannot act on.
func ValidateQwenTTSStream(cfg *Config) error {
	switch QwenTTSStreamOf(cfg) {
	case QwenStreamAuto, QwenStreamOn, QwenStreamOff:
		return nil
	default:
		return fmt.Errorf("unsupported qwen_tts_stream %q; want %s, %s or %s",
			QwenTTSStreamOf(cfg), QwenStreamAuto, QwenStreamOn, QwenStreamOff)
	}
}

// QwenStreamEligible reports whether this host should ask the worker for Stage
// B chunks, given the resolved mode, where speech runs, and what the installed
// package says it was built against.
//
// capable is the worker's own ready.streaming_capable: a worker whose engine
// pin has no PCM callback cannot stream no matter what the config asks for, and
// saying otherwise would only produce a request field it ignores.
//
// auto is deliberately asymmetric between the two places:
//
//   - lan is the Topology B offload target, which exists because the GPU is on
//     another computer. That is the configuration Stage B was measured on and
//     passed (CUDA: TTFA p50 435 ms, RTF max 0.571, zero underruns).
//   - local streams only when the installed package reports backend_hint=cuda.
//     A Metal package is the measured failure — playing its chunks as they
//     arrive underruns mid-word, and the pre-buffer that would prevent it is
//     15.6 s on a 23 s reply, which is worse than not streaming at all. A cpu
//     package, or an external worker with no manifest to ask, is unmeasured and
//     therefore treated as ineligible: this fails closed toward the path that
//     has never produced a dropout.
func QwenStreamEligible(mode, place, backendHint string, capable bool) bool {
	if !capable {
		return false
	}
	switch mode {
	case QwenStreamOff:
		return false
	case QwenStreamOn:
		return true
	}
	if place == TTSPlaceLAN {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(backendHint), QwenBackendCUDA)
}
