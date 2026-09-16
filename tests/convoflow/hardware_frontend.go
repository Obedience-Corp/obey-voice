package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// rawCaptureFrontend keeps a synchronized diagnostic copy of microphone input
// before AEC/NS/AGC processing. The production capture path still receives the
// frontend's processed output; the raw copy is only for acoustic artifacts.
type rawCaptureFrontend struct {
	inner audio.Frontend

	mu           sync.Mutex
	raw          []float32
	traceEnabled bool
	trace        []frontendTraceEvent
	traceStarted time.Time
}

type frontendTraceEvent struct {
	Seq     uint64    `json:"seq"`
	Op      string    `json:"op"`
	Samples []float32 `json:"samples,omitempty"`
	Delay   int       `json:"delay,omitempty"`
	// ObservedAt is the serialized frontend invocation, not ADC capture or
	// speaker presentation time. ElapsedNS uses Go's monotonic clock so wall
	// clock adjustments cannot change intervals within a trace. Old traces
	// have neither field and retain their original timing uncertainty.
	ObservedAt time.Time `json:"observed_at,omitzero"`
	ElapsedNS  int64     `json:"elapsed_ns,omitempty"`
}

func newRawCaptureFrontend(inner audio.Frontend) *rawCaptureFrontend {
	return &rawCaptureFrontend{inner: inner}
}

func (f *rawCaptureFrontend) ProcessCapture(samples []float32) []float32 {
	if f == nil {
		return samples
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw = append(f.raw, samples...)
	f.recordLocked("capture", samples, 0)
	if f.inner == nil {
		return samples
	}
	return f.inner.ProcessCapture(samples)
}

// EnableTrace turns on an ordered, in-memory trace for control diagnostics.
// It is off by default so ordinary recordings do not retain duplicate audio.
func (f *rawCaptureFrontend) EnableTrace(enabled bool) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if enabled && !f.traceEnabled {
		f.traceStarted = time.Now()
	}
	f.traceEnabled = enabled
	if !enabled {
		f.trace = nil
	}
	f.mu.Unlock()
}

func (f *rawCaptureFrontend) recordLocked(op string, samples []float32, delay int) {
	if !f.traceEnabled {
		return
	}
	observedAt := time.Now()
	f.trace = append(f.trace, frontendTraceEvent{
		Seq: uint64(len(f.trace) + 1), Op: op,
		ObservedAt: observedAt, ElapsedNS: observedAt.Sub(f.traceStarted).Nanoseconds(),
		Samples: append([]float32(nil), samples...), Delay: delay,
	})
}

// WriteTrace writes JSONL with copied sample payloads and monotonic sequence
// numbers. The caller opts into this only for local diagnostic artifacts.
func (f *rawCaptureFrontend) WriteTrace(path string) error {
	if f == nil {
		return fmt.Errorf("nil frontend")
	}
	f.mu.Lock()
	events := append([]frontendTraceEvent(nil), f.trace...)
	f.mu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func (f *rawCaptureFrontend) TraceSnapshot() []frontendTraceEvent {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]frontendTraceEvent, len(f.trace))
	for i, event := range f.trace {
		out[i] = event
		out[i].Samples = append([]float32(nil), event.Samples...)
	}
	return out
}

// RawSnapshot returns an independent copy of all pre-frontend samples seen so
// far. It is safe to call while the capture callback continues publishing.
func (f *rawCaptureFrontend) RawSnapshot() []float32 {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float32(nil), f.raw...)
}

func (f *rawCaptureFrontend) PushPlaybackReference(samples []float32) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("reference", samples, 0)
	if f.inner != nil {
		f.inner.PushPlaybackReference(samples)
	}
}

func (f *rawCaptureFrontend) SetReferenceDelay(samples int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("delay", nil, samples)
	if f.inner == nil {
		return
	}
	if delayed, ok := f.inner.(audio.ReferenceDelayer); ok {
		delayed.SetReferenceDelay(samples)
	}
}

func (f *rawCaptureFrontend) InputGain() float64 {
	if f == nil {
		return 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inner != nil {
		if gain, ok := f.inner.(audio.GainReporter); ok {
			return gain.InputGain()
		}
	}
	return 1
}

func (f *rawCaptureFrontend) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked("close", nil, 0)
	if f.inner == nil {
		return nil
	}
	return f.inner.Close()
}

// replayFrontendTrace replays a saved trace through an existing frontend,
// preserving operation order and sample copies. It is intentionally generic
// so diagnostics can compare AEC/NS implementations without devices.
func replayFrontendTrace(path string, target audio.Frontend) error {
	if target == nil {
		return fmt.Errorf("nil replay frontend")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var expected uint64
	for scanner.Scan() {
		var event frontendTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		if event.Seq != expected+1 {
			return fmt.Errorf("trace sequence %d after %d", event.Seq, expected)
		}
		expected = event.Seq
		samples := append([]float32(nil), event.Samples...)
		switch event.Op {
		case "capture":
			target.ProcessCapture(samples)
		case "reference":
			target.PushPlaybackReference(samples)
		case "delay":
			delayer, ok := target.(audio.ReferenceDelayer)
			if !ok {
				return fmt.Errorf("target does not support reference delay")
			}
			delayer.SetReferenceDelay(event.Delay)
		case "close":
			if err := target.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown frontend trace operation %q", event.Op)
		}
	}
	return scanner.Err()
}
