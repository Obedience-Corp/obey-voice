package main

// This file contains an offline diagnostic for the production barge-in VAD.
// It deliberately replays saved microphone WAVs through audio.NewBargeInVAD;
// it does not exercise a device, playback, or a provider.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const diagnosticChunk = 160 // default for legacy WAV-only diagnostics without a callback trace

const legacyDiagnosticCadence = "legacy fixed 160-sample callbacks (no frontend trace)"

type vadDiagnosticSpan struct {
	Start       float64
	End         float64
	Positive    int
	Chunks      int
	MaxPositive int
}

type vadDiagnostic struct {
	Path        string
	Rate        int
	Seconds     float64
	RMS         float64
	Peak        float32
	Threshold   float64
	Positive    int
	Chunks      int
	MaxPositive int
	Spans       []vadDiagnosticSpan
}

// captureCallbackSizes reads the original capture callback boundaries from a
// saved frontend trace. A trace that exists but is malformed is an error: the
// caller must not silently replace recorded boundaries with the legacy cadence.
func captureCallbackSizes(tracePath string, sampleCount int) ([]int, error) {
	file, err := os.Open(tracePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open frontend trace %s: %w", tracePath, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var expected uint64
	var sizes []int
	for scanner.Scan() {
		var event frontendTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode frontend trace %s: %w", tracePath, err)
		}
		if event.Seq != expected+1 {
			return nil, fmt.Errorf("frontend trace %s sequence %d after %d", tracePath, event.Seq, expected)
		}
		expected = event.Seq
		switch event.Op {
		case "capture":
			if len(event.Samples) == 0 {
				return nil, fmt.Errorf("frontend trace %s capture event %d has no samples", tracePath, event.Seq)
			}
			sizes = append(sizes, len(event.Samples))
		case "reference", "delay", "close":
		default:
			return nil, fmt.Errorf("frontend trace %s unknown operation %q", tracePath, event.Op)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read frontend trace %s: %w", tracePath, err)
	}
	if len(sizes) == 0 {
		return nil, fmt.Errorf("frontend trace %s contains no capture events", tracePath)
	}
	var total int
	for _, size := range sizes {
		total += size
	}
	if total != sampleCount {
		return nil, fmt.Errorf("frontend trace %s captures %d samples, WAV contains %d", tracePath, total, sampleCount)
	}
	return sizes, nil
}

// replayBargeVAD uses supplied capture callback sizes when available, or
// a 160-sample diagnostic cadence for legacy WAV-only replays.  A span is a run of positive IsSpeech observations;
// reporting these raw spans makes a threshold comparison reproducible without
// turning this diagnostic into another barge-in policy.
func replayBargeVAD(cfg *config.Config, path string, threshold, armOffset float64, captureSizes ...int) (vadDiagnostic, error) {
	var out vadDiagnostic
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	if rate != audio.SampleRate {
		return out, fmt.Errorf("%s: sample rate %d, want %d", path, rate, audio.SampleRate)
	}
	if cfg == nil {
		return out, fmt.Errorf("%s: nil config", path)
	}
	copyCfg := *cfg
	copyCfg.BargeInVADThreshold = threshold
	vad, err := audio.NewBargeInVAD(&copyCfg)
	if err != nil {
		return out, fmt.Errorf("create barge VAD for %s at %.2f: %w", path, threshold, err)
	}
	defer vad.Delete()
	out = vadDiagnostic{Path: path, Rate: rate, Seconds: float64(len(samples)) / float64(rate), Threshold: config.BargeInVADThresholdOf(&copyCfg)}
	if len(captureSizes) == 0 {
		for remaining := len(samples); remaining > 0; {
			size := min(diagnosticChunk, remaining)
			captureSizes = append(captureSizes, size)
			remaining -= size
		}
	} else {
		var total int
		for _, size := range captureSizes {
			if size <= 0 || size > len(samples)-total {
				return out, fmt.Errorf("invalid capture callback size %d at sample %d", size, total)
			}
			total += size
		}
		if total != len(samples) {
			return out, fmt.Errorf("capture callback sizes cover %d samples, WAV contains %d", total, len(samples))
		}
	}
	var sum float64
	var current *vadDiagnosticSpan
	i := 0
	for _, size := range captureSizes {
		end := i + size
		chunk := samples[i:end]
		for _, sample := range chunk {
			v := math.Abs(float64(sample))
			sum += v * v
			if float32(v) > out.Peak {
				out.Peak = float32(v)
			}
		}
		out.Chunks++
		// The production watcher clears its VAD on every capture callback until
		// armAt. Skip those callbacks here and clear once at the arm boundary so
		// the saved initial user utterance cannot prime the replay.
		armed := float64(i)/float64(rate) >= armOffset
		if armed {
			vad.AcceptWaveform(chunk)
		}
		positive := armed && vad.IsSpeech()
		if positive {
			out.Positive++
			if current == nil {
				current = &vadDiagnosticSpan{Start: float64(i) / float64(rate)}
			}
			current.End = float64(end) / float64(rate)
			current.Positive++
			if current.Positive > current.MaxPositive {
				current.MaxPositive = current.Positive
			}
		} else if current != nil {
			out.Spans = append(out.Spans, *current)
			current = nil
		}
		i = end
	}
	if current != nil {
		out.Spans = append(out.Spans, *current)
	}
	if len(samples) != 0 {
		out.RMS = math.Sqrt(sum / float64(len(samples)))
	}
	out.MaxPositive = 0
	for _, span := range out.Spans {
		if span.MaxPositive > out.MaxPositive {
			out.MaxPositive = span.MaxPositive
		}
	}
	return out, nil
}

func formatVADDiagnostic(d vadDiagnostic) string {
	spans := make([]string, 0, len(d.Spans))
	for _, s := range d.Spans {
		spans = append(spans, fmt.Sprintf("%.3f-%.3f(%d)", s.Start, s.End, s.Positive))
	}
	return fmt.Sprintf("%s threshold=%.2f seconds=%.3f rms=%.6f peak=%.6f positive=%d/%d max_run=%d spans=[%s]", filepath.Base(d.Path), d.Threshold, d.Seconds, d.RMS, d.Peak, d.Positive, d.Chunks, d.MaxPositive, strings.Join(spans, ","))
}

func diagnosticEnvFloat(name string, fallback float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", name, value, err)
	}
	return f, nil
}
