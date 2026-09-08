package stt

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type livePreviewKey struct{}

// WithLivePreview requests provisional words until explicit capture EOF. A
// manual Ask recording must survive pauses and never finish an agent turn.
func WithLivePreview(ctx context.Context) context.Context {
	return context.WithValue(ctx, livePreviewKey{}, true)
}

// previewSamples supplies bounded input gain for quiet USB/lapel microphones.
// Neural VAD still decides speech; digital silence stays zero and background
// noise is never promoted by an unbounded normalization factor.
func previewSamples(samples []float32) []float32 {
	rms := frameRMS(samples)
	gain := 1.0
	if rms > 0 {
		gain = math.Min(8, math.Max(1, 0.04/rms))
	}
	out := make([]float32, len(samples))
	for i, sample := range samples {
		out[i] = float32(math.Max(-1, math.Min(1, float64(sample)*gain)))
	}
	return out
}

// runPreviewLoop gives offline recognizers a rolling preview. Decodes run on
// this one goroutine, so partial and final requests never race the recognizer.
// VAD endpoints commit phrases to the preview, not to conversation history;
// only the client's EOF emits FinalTranscript.
func runPreviewLoop(ctx context.Context, deps offlineLoopDeps, events chan<- Event) {
	deps.seg.Reset()
	sendEvent(ctx, events, PhaseEvent{Phase: "listening"})
	var pending []float32
	var committed, lastPreview string
	var heard bool
	var levels levelEmitter
	var sincePreview int
	var lastDecode time.Time
	const previewInterval = audio.SampleRate * 3 / 4
	const maxPhrase = audio.SampleRate * 15
	const preRoll = audio.SampleRate * 3 / 10
	join := func(text string) string { return strings.TrimSpace(committed + " " + text) }
	decode := func(samples []float32, finalPhrase bool) bool {
		if len(samples) < minSpeechSamples || !audibleSpeech(samples) {
			return true
		}
		text, err := deps.transcribe(samples)
		if err != nil {
			sendEvent(ctx, events, Failure{Err: err})
			return false
		}
		preview := join(text)
		if preview != "" && preview != lastPreview {
			if !sendEvent(ctx, events, PartialTranscript{Text: preview}) {
				return false
			}
			lastPreview = preview
		}
		if finalPhrase {
			committed = preview
		}
		lastDecode = time.Now()
		sincePreview = 0
		return true
	}
	for {
		if ctx.Err() != nil {
			return
		}
		read := readLoopFrame(ctx, deps.frames)
		if read.err != nil {
			sendEvent(ctx, events, Failure{Err: read.err})
			return
		}
		if read.ready && len(read.frame.Samples) > 0 {
			samples := previewSamples(read.frame.Samples)
			levels.maybeEmit(events, samples)
			deps.seg.AcceptWaveform(samples)
			pending = append(pending, samples...)
			sincePreview += len(samples)
			heard = heard || deps.seg.IsSpeech()
			if !heard && len(pending) > preRoll {
				pending = append([]float32(nil), pending[len(pending)-preRoll:]...)
			}
		}
		if read.eof {
			deps.seg.Flush()
		}
		var finalized []float32
		for {
			segment, ok := deps.seg.NextSegment()
			if !ok {
				break
			}
			finalized = append(finalized, segment...)
		}
		if len(finalized) > 0 {
			if !decode(finalized, true) {
				return
			}
			pending = nil
			heard = false
		} else if heard && (read.eof || len(pending) >= maxPhrase) {
			if !decode(pending, true) {
				return
			}
			pending = nil
			heard = false
			deps.seg.Reset()
		} else if heard && sincePreview >= previewInterval && time.Since(lastDecode) >= 500*time.Millisecond {
			if !decode(pending, false) {
				return
			}
		}
		if read.eof {
			sendEvent(ctx, events, FinalTranscript{Text: committed})
			return
		}
		if !read.ready {
			time.Sleep(noFramePoll)
		}
	}
}
