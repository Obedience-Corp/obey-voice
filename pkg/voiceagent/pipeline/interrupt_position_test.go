package pipeline

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// positionCapture uses the production ring's absolute timeline and trimming.
// Its narrow subscription seam supplies controlled callback frames without a
// microphone. Buffered frames retain their capture positions if consumption
// is delayed; later writes continue on that same timeline.
type positionCapture struct {
	*fakeCapture
	ring  *audio.RingBuffer
	mu    sync.Mutex
	frame chan audio.Frame
	kept  []uint64
}

func newPositionCapture() *positionCapture {
	return &positionCapture{fakeCapture: newFakeCapture(), ring: audio.NewRingBuffer(audio.SampleRate * 30)}
}
func (c *positionCapture) SubscribeFrames(buffer int) (int, <-chan audio.Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frame = make(chan audio.Frame, buffer)
	return 1, c.frame
}
func (c *positionCapture) Unsubscribe(int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frame != nil {
		close(c.frame)
		c.frame = nil
	}
}
func (*positionCapture) InputGain() float64 { return 6 }
func (c *positionCapture) KeepFrom(offset uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kept = append(c.kept, offset)
	c.ring.KeepFrom(offset)
}
func (c *positionCapture) publish(samples []float32, known bool) uint64 {
	offset := c.ring.WriteWithPosition(samples)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frame != nil {
		c.frame <- audio.Frame{Samples: samples, SampleRate: audio.SampleRate, Channels: 1,
			SampleOffset: offset, SamplePositionKnown: known, SourceKind: audio.SourceLive}
	}
	return offset
}

func positionPCM(n int, amplitude float32) []float32 {
	pcm := make([]float32, n)
	for i := range pcm {
		pcm[i] = amplitude
	}
	return pcm
}

type positionVerdictVAD struct {
	verdicts []audibilityVerdict
	i        int
	current  audibilityVerdict
	entered  chan struct{}
	release  <-chan struct{}
}

func (v *positionVerdictVAD) AcceptWaveform([]float32) {
	if v.i == 0 && v.entered != nil {
		close(v.entered)
		<-v.release
	}
	v.current = v.verdicts[v.i]
	v.i++
}
func (v *positionVerdictVAD) IsSpeech() bool         { return v.current.speech }
func (v *positionVerdictVAD) IsSpeechDetected() bool { return v.current.queued }
func (v *positionVerdictVAD) Pop()                   { v.current.queued = false }
func (v *positionVerdictVAD) Clear()                 { v.current = audibilityVerdict{} }

// The fourth callback is in a playback gap; subsequent playback resumes. Only
// the controller accesses this sequence, so it provides exact callback ordering
// without a sleep or a concurrent mutation of detector state.
type positionGapPlayback struct{ callbacks int }

func (p *positionGapPlayback) IsPlaying() bool {
	p.callbacks++
	return p.callbacks != 4
}

func TestInterruptPositionSurvivesSkippedPauseTail(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "positioned_gap"
		if !known {
			name = "unknown_gap_disables_trim"
		}
		t.Run(name, func(t *testing.T) {
			capture := newPositionCapture()
			vad := &positionVerdictVAD{verdicts: make([]audibilityVerdict, 6)}
			for i := range vad.verdicts {
				vad.verdicts[i].speech = true
			}
			ctrl := newTestController(capture, vad, &positionGapPlayback{}, bargeInMinSpeechChunks)
			ctrl.tailGuard = time.Hour // fourth callback is certainly inside the guard
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			watch := ctrl.watchWithDone(ctx, armedNow())
			t.Cleanup(func() { cancel(); <-watch.done })
			for i := range 7 {
				capture.publish(positionPCM(160, .018), i != 3 || known)
			}
			select {
			case request := <-watch.requests:
				if request.OnsetSampleOffset != 0 || request.OnsetPositionKnown != known {
					t.Fatalf("gap changed candidate position: %+v, want first callback known=%v", request, known)
				}
			case <-ctx.Done():
				t.Fatal("pause guard erased the spanning speech run")
			}
		})
	}
}

func TestInterruptPositionKeepsOnsetPreRollAndLaterArrivals(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		name := "immediate_watcher"
		if delayed {
			name = "delayed_watcher"
		}
		t.Run(name, func(t *testing.T) {
			capture := newPositionCapture()
			capture.ring.Write(positionPCM(16000, .04)) // older assistant words
			preRoll := positionPCM(4800, .001)
			capture.ring.Write(preRoll)
			vad := &positionVerdictVAD{verdicts: make([]audibilityVerdict, 6)}
			for i := range vad.verdicts {
				vad.verdicts[i].speech = true
			}
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			if delayed {
				vad.entered, vad.release = make(chan struct{}), release
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			playing := &alwaysPlaying{}
			playing.playing.Store(true)
			watch := newTestController(capture, vad, playing, bargeInMinSpeechChunks).watchWithDone(ctx, armedNow())
			t.Cleanup(func() { unblock(); cancel(); <-watch.done })
			want := slices.Clone(preRoll)
			for i, amplitude := range []float32{.006, .018, .018, .006, .018, .018} {
				frame := positionPCM(160, amplitude)
				capture.publish(frame, true)
				want = append(want, frame...)
				if delayed && i == 0 {
					select {
					case <-vad.entered:
					case <-ctx.Done():
						t.Fatal("watcher did not reach first captured frame")
					}
				}
			}
			// More of the utterance is already buffered when the watcher catches
			// up. A read-time or last-N-samples trim would lose its opening.
			later := positionPCM(6400, .025)
			capture.ring.Write(later)
			want = append(want, later...)
			unblock()
			var request interruptRequest
			select {
			case request = <-watch.requests:
			case <-ctx.Done():
				t.Fatal("positioned neural speech did not interrupt")
			}
			<-watch.done
			if !request.OnsetPositionKnown || request.OnsetSampleOffset != 20800 {
				t.Fatalf("onset=%+v, want first neural candidate at 20800, including its energy dip", request)
			}
			p := &Pipeline{Capture: capture, CapturePreRoll: 300 * time.Millisecond}
			p.trimInterruptedCapture(request)
			// Writes after trimming must also survive the handoff.
			after := positionPCM(320, .03)
			capture.ring.Write(after)
			want = append(want, after...)
			got := capture.ring.Read(capture.ring.Available())
			if !slices.Equal(got, want) {
				t.Fatalf("retained %d samples, want exact %d onset/pre-roll/later PCM", len(got), len(want))
			}
			if capture.ResetCount() != 0 || len(capture.subs) != 0 {
				t.Fatal("positioned handoff reset capture or used legacy subscription")
			}
		})
	}
}

func TestInterruptPositionUsesCompletedRunAndRejectsUnknownPosition(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amplitudes []float32
		verdicts   []audibilityVerdict
		unknown    int
		wantOffset uint64
		wantKnown  bool
	}{
		{"queued_short_utterance", []float32{.018, .018, 0}, []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}}, -1, 0, true},
		{"quiet_completed_run_then_real", []float32{.006, .006, 0, .018, .018, 0}, []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}, {speech: true}, {speech: true}, {queued: true}}, -1, 480, true},
		{"unknown_first_candidate", []float32{.018, .018, 0}, []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}}, 0, 0, false},
		{"unknown_inside_candidate", []float32{.018, .018, 0}, []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}}, 1, 0, false},
		{"unknown_completing_callback", []float32{.018, .018, 0}, []audibilityVerdict{{speech: true}, {speech: true}, {queued: true}}, 2, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := newPositionCapture()
			vad := &positionVerdictVAD{verdicts: tc.verdicts}
			playing := &alwaysPlaying{}
			playing.playing.Store(true)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			watch := newTestController(capture, vad, playing, bargeInMinSpeechChunks).watchWithDone(ctx, armedNow())
			t.Cleanup(func() { cancel(); <-watch.done })
			for i, amplitude := range tc.amplitudes {
				capture.publish(positionPCM(160, amplitude), i != tc.unknown)
			}
			select {
			case request := <-watch.requests:
				if request.OnsetPositionKnown != tc.wantKnown || request.OnsetSampleOffset != tc.wantOffset {
					t.Fatalf("onset=%+v, want offset=%d known=%v", request, tc.wantOffset, tc.wantKnown)
				}
			case <-ctx.Done():
				t.Fatal("completed audible utterance did not interrupt")
			}
		})
	}
}

func TestResponseBargeTrimsOnlyOrdinaryCapture(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "ordinary"
		if active {
			name = "active_stt_owns_input"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			capture := newPositionCapture()
			old := positionPCM(16000, .04)
			preRoll := positionPCM(4800, .001)
			capture.ring.Write(old)
			capture.ring.Write(preRoll)
			player := newFakePlayer(10 * time.Second)
			defer player.Close()
			vad := &positionVerdictVAD{verdicts: make([]audibilityVerdict, bargeInMinSpeechChunks)}
			for i := range vad.verdicts {
				vad.verdicts[i].speech = true
			}
			p := &Pipeline{Capture: capture, CapturePreRoll: 300 * time.Millisecond, BargeInVAD: vad,
				TTS: &fakeTTS{}, Player: player, Events: events.NewBus()}
			if active {
				p.captureSession = &fakeSTTSession{}
			}
			started := make(chan struct{}, 1)
			remove := p.Events.SubscribeAll(func(event events.Event) {
				if _, ok := event.(events.SpeakingStarted); ok {
					started <- struct{}{}
				}
			})
			defer remove()
			done := make(chan bool, 1)
			go func() {
				metrics := newTurnMetrics()
				_, interrupted, _ := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{"A long assistant response."}, nil), true, metrics, p.newTurnConductor(metrics), nil)
				done <- interrupted
			}()
			joined := false
			t.Cleanup(func() {
				cancel()
				if !joined {
					<-done
				}
			})
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("response never started playback")
			}
			// Exercise the unchanged production arm interval after its actual
			// SpeakingStarted event; no direct interrupt request is injected.
			select {
			case <-time.After(bargeInArmDelay):
			case <-ctx.Done():
				t.Fatal("response ended before arming")
			}
			want := slices.Clone(preRoll)
			if active {
				want = append(slices.Clone(old), preRoll...)
			}
			for range bargeInMinSpeechChunks {
				frame := positionPCM(960, .03)
				capture.publish(frame, true)
				want = append(want, frame...)
			}
			select {
			case interrupted := <-done:
				joined = true
				if !interrupted {
					t.Fatal("production response did not observe barge-in")
				}
			case <-ctx.Done():
				// Barge-in itself cancels ctx before the response goroutine joins.
				select {
				case interrupted := <-done:
					joined = true
					if !interrupted {
						t.Fatal("response canceled without a barge-in")
					}
				case <-time.After(time.Second):
					t.Fatal("response failed to join after interruption")
				}
			}
			got := capture.ring.Read(capture.ring.Available())
			if !slices.Equal(got, want) {
				t.Fatalf("production handoff retained %d samples, want exact %d (active STT=%v)", len(got), len(want), active)
			}
			if active && len(capture.kept) != 0 {
				t.Fatal("response externally trimmed an active STT owner's buffer")
			}
		})
	}
}

func TestInterruptTrimFallsBackWithoutPositionAndClampsPreRoll(t *testing.T) {
	capture := newPositionCapture()
	pcm := positionPCM(1000, .03)
	capture.ring.Write(pcm)
	p := &Pipeline{Capture: capture, CapturePreRoll: 300 * time.Millisecond}
	p.trimInterruptedCapture(interruptRequest{OnsetSampleOffset: 500})
	if len(capture.kept) != 0 {
		t.Fatal("unknown onset trimmed capture")
	}
	p.trimInterruptedCapture(interruptRequest{OnsetSampleOffset: 500, OnsetPositionKnown: true})
	if !slices.Equal(capture.kept, []uint64{0}) || !slices.Equal(capture.ring.Read(1000), pcm) {
		t.Fatal("pre-roll subtraction wrapped or lost early input")
	}
	legacy := newFakeCapture()
	p.Capture = legacy
	p.trimInterruptedCapture(interruptRequest{OnsetSampleOffset: 500, OnsetPositionKnown: true})
	if legacy.ResetCount() != 0 {
		t.Fatal("legacy capture was reset instead of preserved")
	}
}
