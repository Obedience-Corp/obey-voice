package pipeline

import (
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type sourceEvidenceCapture struct {
	frames       chan audio.Frame
	chunks       chan []float32
	unsubscribed chan int
	frameCalls   int
	legacyCalls  int
}

func newSourceEvidenceCapture() *sourceEvidenceCapture {
	return &sourceEvidenceCapture{frames: make(chan audio.Frame, 32), chunks: make(chan []float32, 32), unsubscribed: make(chan int, 1)}
}
func (c *sourceEvidenceCapture) Subscribe(int) (int, <-chan []float32) {
	c.legacyCalls++
	return 11, c.chunks
}
func (c *sourceEvidenceCapture) SubscribeFrames(int) (int, <-chan audio.Frame) {
	c.frameCalls++
	return 22, c.frames
}
func (c *sourceEvidenceCapture) Unsubscribe(id int) { c.unsubscribed <- id }
func (*sourceEvidenceCapture) Reset()               {}
func (*sourceEvidenceCapture) InputGain() float64   { return 6 }

// Deliberately hides SubscribeFrames while retaining the same PCM and gain.
type legacyEvidenceCapture struct{ capture *sourceEvidenceCapture }

func (c legacyEvidenceCapture) Subscribe(n int) (int, <-chan []float32) {
	return c.capture.Subscribe(n)
}
func (c legacyEvidenceCapture) Unsubscribe(id int) { c.capture.Unsubscribe(id) }
func (legacyEvidenceCapture) Reset()               {}
func (legacyEvidenceCapture) InputGain() float64   { return 6 }

type sourceEvidenceVAD struct {
	verdicts []audibilityVerdict
	received [][]float32
	current  audibilityVerdict
	cleared  chan struct{}
}

func (v *sourceEvidenceVAD) AcceptWaveform(samples []float32) {
	v.current = v.verdicts[len(v.received)]
	v.received = append(v.received, slices.Clone(samples))
}
func (v *sourceEvidenceVAD) IsSpeech() bool         { return v.current.speech }
func (v *sourceEvidenceVAD) IsSpeechDetected() bool { return v.current.queued }
func (v *sourceEvidenceVAD) Clear() {
	v.current = audibilityVerdict{}
	if v.cleared != nil {
		v.cleared <- struct{}{}
	}
}

func evidenceFrame(sequence int64, offset uint64, count int, amplitude float32) audio.Frame {
	samples := make([]float32, count)
	for i := range samples {
		samples[i] = amplitude
	}
	at := time.Unix(100, sequence*int64(time.Second))
	return audio.Frame{
		Samples: samples, SampleRate: audio.SampleRate, Channels: 1, Sequence: sequence,
		Duration: audio.SamplesDuration(count), SourceKind: audio.SourceLive,
		CaptureSpans: []audio.CaptureSpan{{SampleOffset: offset, SampleCount: count, DeviceGeneration: 7,
			CallbackAt: at, EstimatedStart: at.Add(-audio.SamplesDuration(count))}},
	}
}

func receiveSourceEvidence(t *testing.T, watch interruptWatch) interruptRequest {
	t.Helper()
	select {
	case <-watch.done:
	case <-time.After(time.Second):
		t.Fatal("interrupt watcher did not finish")
	}
	select {
	case req := <-watch.requests:
		return req
	default:
		t.Fatal("no qualifying interruption")
		return interruptRequest{}
	}
}

func TestInterruptSourceTypedAndLegacyPreserveQualificationAndCallbackPCM(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amplitudes []float32
		verdicts   []audibilityVerdict
	}{
		{name: "sustained", amplitudes: []float32{.0006, .0006, 0, .018, .018, .018, .018, .018, .018},
			verdicts: []audibilityVerdict{{speech: true}, {speech: true}, {}, {speech: true}, {speech: true}, {speech: true}, {speech: true}, {speech: true}, {speech: true}}},
		{name: "completed_shortcut", amplitudes: []float32{.018, .018, 0},
			verdicts: []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frames []audio.Frame
			offset := uint64(4000)
			for i, amplitude := range tc.amplitudes {
				count := []int{160, 960, 320}[i%3]
				frames = append(frames, evidenceFrame(int64(i+1), offset, count, amplitude))
				offset += uint64(count)
			}
			for _, typed := range []bool{false, true} {
				t.Run(map[bool]string{false: "legacy", true: "typed"}[typed], func(t *testing.T) {
					capture := newSourceEvidenceCapture()
					var monitor CaptureMonitor = legacyEvidenceCapture{capture}
					if typed {
						monitor = capture
					}
					vad := &sourceEvidenceVAD{verdicts: tc.verdicts}
					playing := &alwaysPlaying{}
					playing.playing.Store(true)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					watch := newTestController(monitor, vad, playing, bargeInMinSpeechChunks).watchWithDone(ctx, armedNow())
					for _, frame := range frames {
						if typed {
							capture.frames <- frame
						} else {
							capture.chunks <- frame.Samples
						}
					}
					req := receiveSourceEvidence(t, watch)
					if req.Reason != "barge_in" || req.At.IsZero() || len(vad.received) != len(frames) {
						t.Fatalf("qualification changed: req=%+v accepted=%d want=%d", req, len(vad.received), len(frames))
					}
					for i, frame := range frames {
						if !slices.Equal(vad.received[i], frame.Samples) {
							t.Fatalf("callback %d was changed or rechunked", i)
						}
					}
					last := frames[len(frames)-1]
					if req.Source.CallbackSamples != len(last.Samples) {
						t.Fatalf("qualification samples = %d, want %d", req.Source.CallbackSamples, len(last.Samples))
					}
					if typed {
						if capture.frameCalls != 1 || capture.legacyCalls != 0 || <-capture.unsubscribed != 22 {
							t.Fatal("typed monitor used a second or legacy subscription")
						}
						if !req.Source.CallbackKnown || req.Source.UnknownObserved || req.Source.CallbackSequence != last.Sequence || !reflect.DeepEqual(req.Source.CallbackSpans, last.CaptureSpans) {
							t.Fatalf("qualifying callback coverage = %+v, want %+v", req.Source, last)
						}
						// Request evidence must outlive the monitor's backing slice.
						expected := slices.Clone(req.Source.CallbackSpans)
						last.CaptureSpans[0].SampleOffset++
						if !reflect.DeepEqual(req.Source.CallbackSpans, expected) {
							t.Fatal("request aliases capture metadata")
						}
					} else {
						if capture.frameCalls != 0 || capture.legacyCalls != 1 || <-capture.unsubscribed != 11 {
							t.Fatal("legacy subscription changed")
						}
						if req.Source.CallbackKnown || !req.Source.UnknownObserved || req.Source.CallbackSpans != nil {
							t.Fatalf("legacy source must remain explicitly unknown: %+v", req.Source)
						}
					}
				})
			}
		})
	}
}

func TestInterruptSourceRetainsEarlierLossAndUnknownThroughArmGuard(t *testing.T) {
	capture := newSourceEvidenceCapture()
	vad := &sourceEvidenceVAD{verdicts: []audibilityVerdict{{speech: true}, {speech: true}}, cleared: make(chan struct{}, 4)}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var armAt atomic.Int64 // first frame is observed while unarmed
	watch := newTestController(capture, vad, playing, 2).watchWithDone(ctx, &armAt)
	missing := evidenceFrame(1, 0, 960, .018)
	missing.CaptureSpans = nil
	missing.CaptureGap = audio.CaptureGap{SubscriptionDroppedSamples: 1920, SubscriptionDroppedCallbacks: 2, SubscriptionDeviceDiscontinuity: true}
	capture.frames <- missing
	select {
	case <-vad.cleared:
	case <-time.After(time.Second):
		t.Fatal("unarmed frame was not processed")
	}
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	first := evidenceFrame(4, 2880, 960, .018)
	first.CaptureSpans[0].DeviceDiscontinuity = true
	first.CaptureGap = audio.CaptureGap{OverflowSamples: 160, DiscardedSamples: 320, Reset: true,
		SubscriptionDroppedSamples: 960, SubscriptionDroppedCallbacks: 1}
	capture.frames <- first
	last := evidenceFrame(5, 3840, 960, .018)
	capture.frames <- last
	req := receiveSourceEvidence(t, watch)
	wantGap := audio.CaptureGap{OverflowSamples: 160, DiscardedSamples: 320, Reset: true,
		SubscriptionDroppedSamples: 2880, SubscriptionDroppedCallbacks: 3, SubscriptionDeviceDiscontinuity: true}
	if req.Source.Gap != wantGap || !req.Source.DeviceDiscontinuity || !req.Source.UnknownObserved {
		t.Fatalf("earlier evidence was lost: %+v, want gap %+v", req.Source, wantGap)
	}
	if !req.Source.CallbackKnown || !reflect.DeepEqual(req.Source.CallbackSpans, last.CaptureSpans) || len(vad.received) != 2 {
		t.Fatalf("final clean callback or existing arm guard changed: req=%+v accepted=%d", req.Source, len(vad.received))
	}
}

func TestInterruptSourceTypedCloseAndCancellationJoin(t *testing.T) {
	for _, closeSource := range []bool{true, false} {
		t.Run(map[bool]string{true: "source_closed", false: "canceled"}[closeSource], func(t *testing.T) {
			capture := newSourceEvidenceCapture()
			playing := &alwaysPlaying{}
			playing.playing.Store(true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			watch := newTestController(capture, &sourceEvidenceVAD{}, playing, 6).watchWithDone(ctx, armedNow())
			if closeSource {
				close(capture.frames)
			} else {
				cancel()
			}
			select {
			case <-watch.done:
			case <-time.After(time.Second):
				t.Fatal("typed watcher failed to join")
			}
			if <-capture.unsubscribed != 22 {
				t.Fatal("wrong typed subscription removed")
			}
			select {
			case req := <-watch.requests:
				t.Fatalf("lifecycle exit manufactured request: %+v", req)
			default:
			}
		})
	}
}
