package pipeline

import (
	"context"
	"testing"
	"time"
)

type audibilityCapture struct{ *fakeCapture }

func (*audibilityCapture) InputGain() float64 { return 6 }

type audibilityVerdict struct {
	speech bool
	queued bool
}

// Neural verdicts deliberately do not depend on amplitude: a false positive
// can mark quiet echo or digital silence as speech. Only the controller reads
// current; accepted lets tests sequence actual capture callbacks without sleeps.
type audibilityVAD struct {
	verdicts chan audibilityVerdict
	accepted chan struct{}
	current  audibilityVerdict
}

func (v *audibilityVAD) AcceptWaveform([]float32) {
	v.current = <-v.verdicts
	v.accepted <- struct{}{}
}
func (v *audibilityVAD) IsSpeech() bool         { return v.current.speech }
func (v *audibilityVAD) IsSpeechDetected() bool { return v.current.queued }
func (v *audibilityVAD) Clear()                 { v.current = audibilityVerdict{} }

type audibilityHarness struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	capture *audibilityCapture
	vad     *audibilityVAD
	control *interruptController
	watch   interruptWatch
}

func newAudibilityHarness(t *testing.T, playing bool) *audibilityHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	capture := &audibilityCapture{newFakeCapture()}
	vad := &audibilityVAD{verdicts: make(chan audibilityVerdict, 1), accepted: make(chan struct{}, 1)}
	playback := &alwaysPlaying{}
	playback.playing.Store(playing)
	control := newTestController(capture, vad, playback, bargeInMinSpeechChunks)
	h := &audibilityHarness{t: t, ctx: ctx, cancel: cancel, capture: capture, vad: vad, control: control}
	h.watch = control.watchWithDone(ctx, armedNow())
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.watch.done:
		case <-time.After(time.Second):
			t.Error("audibility controller did not join after cancellation")
		}
	})
	return h
}

func (h *audibilityHarness) feed(amplitude float32, speech, queued bool) {
	h.t.Helper()
	// 160 samples at 16kHz model one 10ms callback. Values are already gained:
	// .006 represents raw .001 echo, .018 represents raw .003 quiet speech.
	samples := make([]float32, 160)
	for i := range samples {
		samples[i] = amplitude
	}
	h.vad.verdicts <- audibilityVerdict{speech: speech, queued: queued}
	h.capture.Publish(samples)
	select {
	case <-h.vad.accepted:
	case <-h.watch.done:
		// The expected final callback can acknowledge and trip before this
		// goroutine wakes. Its acknowledgement still proves it was consumed.
		select {
		case <-h.vad.accepted:
		default:
			h.t.Fatal("controller stopped before consuming the next scripted frame")
		}
	case <-h.ctx.Done():
		h.t.Fatal("controller did not consume scripted capture frame")
	}
}

func (h *audibilityHarness) expectTrip() {
	h.t.Helper()
	select {
	case request := <-h.watch.requests:
		if request.Reason != "barge_in" {
			h.t.Fatalf("interrupt reason=%q", request.Reason)
		}
	case <-h.ctx.Done():
		h.t.Fatal("audible neural speech did not interrupt")
	}
}

func (h *audibilityHarness) finishWithoutTrip() {
	h.t.Helper()
	// Reaching the next AcceptWaveform proves the preceding callback finished
	// its decision. This silent neural-negative boundary cannot authorize a trip.
	h.feed(0, false, false)
	h.cancel()
	<-h.watch.done
	select {
	case request := <-h.watch.requests:
		h.t.Fatalf("inaudible or insufficient speech interrupted: %+v", request)
	default:
	}
}

func TestInterruptAudibilityDistinguishesGainedEchoAndQuietSpeech(t *testing.T) {
	t.Run("six_neural_echo_votes_are_not_audible", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		for range bargeInMinSpeechChunks {
			h.feed(.006, true, false)
		}
		h.finishWithoutTrip()
	})
	t.Run("six_neural_votes_with_energy_dips_still_interrupt", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		// RMS across the run exceeds the existing .002*6 floor, although
		// only three callbacks individually exceed it. Do not require six
		// consecutive energy-positive callbacks in addition to neural votes.
		for _, amplitude := range []float32{.018, .006, .018, .006, .018, .006} {
			h.feed(amplitude, true, false)
		}
		h.expectTrip()
	})
}

func TestInterruptAudibilityResetsAtNeuralSilence(t *testing.T) {
	t.Run("old_audible_evidence_cannot_authorize_quiet_echo", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(.018, true, false)
		h.feed(0, false, false)
		for range bargeInMinSpeechChunks {
			h.feed(.006, true, false)
		}
		h.finishWithoutTrip()
	})
	t.Run("speech_separated_by_silence_needs_a_new_neural_streak", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		for range 3 {
			h.feed(.018, true, false)
		}
		h.feed(0, false, false)
		for range 3 {
			h.feed(.018, true, false)
		}
		// Feeding these three more callbacks also proves the first three
		// did not reuse votes from before the pause and trip early.
		for range 3 {
			h.feed(.006, true, false)
		}
		h.expectTrip()
	})
}

func TestInterruptAudibilityGatesQueuedSegments(t *testing.T) {
	t.Run("audible_segment_may_finish_on_silent_callback", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(.018, true, false)
		h.feed(.018, true, false)
		h.feed(0, false, true)
		h.expectTrip()
	})
	t.Run("quiet_queued_segment_cannot_bypass_gate", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(.006, true, false)
		h.feed(.006, true, false)
		h.feed(0, false, true)
		h.finishWithoutTrip()
	})
	t.Run("digital_silence_with_queued_neural_segment_is_rejected", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(0, true, true)
		h.finishWithoutTrip()
	})
	t.Run("later_queued_segment_cannot_reuse_expired_run", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(.018, true, false)
		h.feed(0, false, false)
		h.feed(0, false, true)
		h.finishWithoutTrip()
	})
	t.Run("rejected_queue_cannot_authorize_a_new_short_run", func(t *testing.T) {
		h := newAudibilityHarness(t, true)
		h.feed(.006, true, false)
		h.feed(0, false, true)
		// The rejected segment remains queued while unrelated audible speech
		// begins. Its old queue level must not replace the new run's six votes.
		h.feed(.018, true, true)
		h.feed(.018, true, true)
		h.finishWithoutTrip()
	})
}

func TestInterruptAudibilityPauseNearMissRequiresAudibleSpeech(t *testing.T) {
	for _, tc := range []struct {
		name      string
		amplitude float32
		want      bool
	}{
		{"quiet_echo", .006, false},
		{"quiet_real_speech", .018, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAudibilityHarness(t, false)
			h.feed(tc.amplitude, true, false)
			h.feed(tc.amplitude, true, false)
			h.finishWithoutTrip()
			if got := h.control.sawPauseSpeechWithin(time.Second); got != tc.want {
				t.Fatalf("audible pause near-miss=%t, want %t", got, tc.want)
			}
		})
	}
}
