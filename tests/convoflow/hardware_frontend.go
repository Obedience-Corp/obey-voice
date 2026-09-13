package main

import (
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// rawCaptureFrontend keeps a synchronized diagnostic copy of microphone input
// before AEC/NS/AGC processing. The production capture path still receives the
// frontend's processed output; the raw copy is only for acoustic artifacts.
type rawCaptureFrontend struct {
	inner audio.Frontend

	mu  sync.Mutex
	raw []float32
}

func newRawCaptureFrontend(inner audio.Frontend) *rawCaptureFrontend {
	return &rawCaptureFrontend{inner: inner}
}

func (f *rawCaptureFrontend) ProcessCapture(samples []float32) []float32 {
	if f == nil {
		return samples
	}
	f.mu.Lock()
	f.raw = append(f.raw, samples...)
	f.mu.Unlock()
	if f.inner == nil {
		return samples
	}
	return f.inner.ProcessCapture(samples)
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
	if f != nil && f.inner != nil {
		f.inner.PushPlaybackReference(samples)
	}
}

func (f *rawCaptureFrontend) SetReferenceDelay(samples int) {
	if f == nil || f.inner == nil {
		return
	}
	if delayed, ok := f.inner.(audio.ReferenceDelayer); ok {
		delayed.SetReferenceDelay(samples)
	}
}

func (f *rawCaptureFrontend) InputGain() float64 {
	if f != nil && f.inner != nil {
		if gain, ok := f.inner.(audio.GainReporter); ok {
			return gain.InputGain()
		}
	}
	return 1
}

func (f *rawCaptureFrontend) Close() error {
	if f == nil || f.inner == nil {
		return nil
	}
	return f.inner.Close()
}
