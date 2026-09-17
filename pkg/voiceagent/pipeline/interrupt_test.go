package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// alwaysPlaying is a minimal playbackState that reports playback is active.
type alwaysPlaying struct{ playing atomic.Bool }

func (a *alwaysPlaying) IsPlaying() bool { return a.playing.Load() }

// newTestController wires a controller with small thresholds and an injected
// capture/vad/playback for deterministic tests. tailGuard is zero so pause
// chunks are processed immediately; tail-guard tests set it explicitly.
func newTestController(capture CaptureMonitor, vad VoiceDetector, playback playbackState, minSpeech int) *interruptController {
	return &interruptController{
		capture:    capture,
		vad:        vad,
		playback:   playback,
		armDelay:   bargeInArmDelay,
		minSpeech:  minSpeech,
		bufferSize: bargeInBufferSize,
	}
}

// armed returns an armAt that has already elapsed (barge-in armed now).
func armedNow() *atomic.Int64 {
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	return &armAt
}

func TestInterruptControllerDisabledWhenMissingCollaborators(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	cases := map[string]*interruptController{
		"no capture":  newTestController(nil, vad, playing, 3),
		"no vad":      newTestController(capture, nil, playing, 3),
		"no playback": newTestController(capture, vad, nil, 3),
	}
	for name, c := range cases {
		if ch := c.watch(context.Background(), armedNow()); ch != nil {
			t.Errorf("%s: watch returned non-nil channel, want nil (disabled)", name)
		}
	}
	if got := len(capture.subs); got != 0 {
		t.Fatalf("capture subscriptions = %d, want 0 when disabled", got)
	}
}

func TestInterruptControllerFiresOnSustainedSpeech(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())
	if out == nil {
		t.Fatal("watch returned nil with all collaborators present")
	}

	// Three consecutive speech chunks meet the threshold and trip barge-in.
	for range 3 {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case req := <-out:
		if req.Reason != "barge_in" {
			t.Fatalf("interruptRequest.Reason = %q, want barge_in", req.Reason)
		}
		if req.At.IsZero() {
			t.Fatal("interruptRequest.At is zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not fire after sustained speech")
	}
}

func TestInterruptControllerDoesNotFireBelowThreshold(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	// Two speech chunks then silence — below the 5-chunk threshold, and the
	// silence resets the counter, so no barge-in.
	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(5 * time.Millisecond)
	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(5 * time.Millisecond)
	capture.Publish([]float32{0.0, 0.0}) // silence resets
	time.Sleep(5 * time.Millisecond)

	select {
	case <-out:
		t.Fatal("controller fired below the speech threshold")
	case <-time.After(120 * time.Millisecond):
	}
}

func TestInterruptControllerHonorsArmDelay(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Emission is fenced until armAt; detector state is still collected.
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(time.Hour).UnixNano())
	out := c.watch(ctx, &armAt)

	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired while disarmed (inside arm delay)")
	case <-time.After(80 * time.Millisecond):
	}

	// Now arm and send sustained speech — it must fire.
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	for range 2 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not fire after arming")
	}
}

func TestInterruptControllerUnarmedBeforeFirstPlayback(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// armAt zero = the turn's first playback hasn't happened. Even sustained
	// speech must not trip: thinking-phase barge-in is deliberately off (F3.4).
	var armAt atomic.Int64
	out := c.watch(ctx, &armAt)

	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired before the turn's first playback armed it")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestInterruptControllerCollectsSpeechDuringArmDelay(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var armAt atomic.Int64
	armAt.Store(time.Now().Add(time.Hour).UnixNano())
	out := c.watch(ctx, &armAt)

	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller emitted during the arm fence")
	case <-time.After(80 * time.Millisecond):
	}

	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	capture.Publish([]float32{0.0, 0.0})
	select {
	case req := <-out:
		if req.Reason != "barge_in" {
			t.Fatalf("interruptRequest.Reason = %q, want barge_in", req.Reason)
		}
		if !req.Source.DuringArm {
			t.Fatal("startup collection did not mark DuringArm")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collected arm-window speech did not qualify after the fence")
	}
}

func TestInterruptControllerStartupShortQualifiesAtArmFence(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)
	c := newTestController(capture, vad, playing, bargeInMinSpeechChunks)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(time.Hour).UnixNano())
	out := c.watch(ctx, &armAt)
	for range bargeInStartupMinChunks {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("short startup speech emitted before the arm fence")
	case <-time.After(80 * time.Millisecond):
	}
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	capture.Publish([]float32{0.0, 0.0})
	select {
	case req := <-out:
		if !req.Source.DuringArm {
			t.Fatal("short startup speech was not marked DuringArm")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("short startup speech did not qualify at the arm fence")
	}
}

func TestInterruptControllerFiresOnPauseSpeech(t *testing.T) {
	// The B3 core regression: once armed, speech during a playback pause
	// (queue-drain gap, model pause between segments) must trip barge-in even
	// though nothing is playing.
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{} // playing=false: the turn is mid-pause

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	for range 3 {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case req := <-out:
		if req.Reason != "barge_in" {
			t.Fatalf("interruptRequest.Reason = %q, want barge_in", req.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not fire on speech during a playback pause")
	}
	if !c.sawPauseSpeechWithin(2 * time.Second) {
		t.Fatal("pause speech was not recorded for near-miss retention")
	}
}

func TestInterruptControllerStreakSurvivesPlaybackGap(t *testing.T) {
	// An utterance spanning a queue-drain gap keeps its accumulated streak:
	// chunks inside the tail guard are skipped, not cleared. tailGuard is huge
	// so every pause chunk lands inside the guard window.
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 2)
	c.tailGuard = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	// One speech chunk while playing: streak = 1, below the threshold of 2.
	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(20 * time.Millisecond)

	// Pause. Speech inside the tail guard is swallowed — no trip, no reset.
	playing.playing.Store(false)
	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired on speech inside the pause tail guard")
	case <-time.After(60 * time.Millisecond):
	}
	if got := vad.clearedCount(); got != 0 {
		t.Fatalf("vad.Clear() called %d times while armed, want 0 (streak must survive the gap)", got)
	}

	// Playback resumes; one more speech chunk completes the spanning streak.
	playing.playing.Store(true)
	time.Sleep(20 * time.Millisecond)
	capture.Publish([]float32{0.9, 0.9})

	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("speech streak did not survive the playback gap")
	}
}

func TestInterruptControllerNearMissOnlyFromPauseSpeech(t *testing.T) {
	// Speech heard while audio is playing must not count as near-miss
	// evidence — during playback it can be echo; only pause speech is trusted
	// to keep the capture buffer.
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 100) // never trips
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = c.watch(ctx, armedNow())

	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(20 * time.Millisecond)
	if c.sawPauseSpeechWithin(time.Second) {
		t.Fatal("speech during playback recorded as pause speech")
	}

	playing.playing.Store(false)
	time.Sleep(10 * time.Millisecond)
	capture.Publish([]float32{0.9, 0.9})
	waitForCond(t, func() bool { return c.sawPauseSpeechWithin(time.Second) }, time.Second,
		"pause speech was not recorded as near-miss evidence")
}

func TestArmDelayAppliesOncePerTurn(t *testing.T) {
	// applyPlaybackEvent arms on the turn's first playback only; later
	// playbackStarted events (per-sentence) must not re-impose the delay.
	p := &Pipeline{Events: events.NewBus()}
	metrics := newTurnMetrics()
	var armAt atomic.Int64

	p.applyPlaybackEvent(playbackEvent{kind: playbackStarted}, metrics, &armAt)
	first := armAt.Load()
	if first == 0 {
		t.Fatal("first playbackStarted did not arm barge-in")
	}

	time.Sleep(5 * time.Millisecond)
	p.applyPlaybackEvent(playbackEvent{kind: playbackStarted}, metrics, &armAt)
	if got := armAt.Load(); got != first {
		t.Fatalf("second playbackStarted re-armed: armAt %d -> %d", first, got)
	}
}

// TestBargeInTailGuardDefaultsWhenUnset proves Pipeline.bargeInTailGuard falls
// back to the built-in default when BargeInTailGuard (wired from config
// barge_in_tail_guard_ms in voiceagent.New) is left zero.
func TestBargeInTailGuardDefaultsWhenUnset(t *testing.T) {
	p := &Pipeline{}
	if got := p.bargeInTailGuard(); got != bargeInPauseTailGuardDefault {
		t.Errorf("bargeInTailGuard() = %v, want default %v", got, bargeInPauseTailGuardDefault)
	}
}

// TestBargeInTailGuardHonorsConfiguredOverride proves a configured
// BargeInTailGuard (barge_in_tail_guard_ms) reaches the interrupt controller
// instead of the package default.
func TestBargeInTailGuardHonorsConfiguredOverride(t *testing.T) {
	const configured = 500 * time.Millisecond
	p := &Pipeline{BargeInTailGuard: configured}
	if got := p.bargeInTailGuard(); got != configured {
		t.Errorf("bargeInTailGuard() = %v, want configured %v", got, configured)
	}

	// newInterruptController must thread the same value through, regardless
	// of whether the other collaborators are wired (enabled() is checked
	// separately by watch/watchWithDone, not by this constructor).
	ctrl := p.newInterruptController()
	if ctrl.tailGuard != configured {
		t.Errorf("newInterruptController() tailGuard = %v, want configured %v", ctrl.tailGuard, configured)
	}
}

// TestBargeInSpeakerVerdictMaxAgeDefaultsWhenUnset mirrors
// TestBargeInTailGuardDefaultsWhenUnset for the S3 staleness bound.
func TestBargeInSpeakerVerdictMaxAgeDefaultsWhenUnset(t *testing.T) {
	p := &Pipeline{}
	if got := p.bargeInSpeakerVerdictMaxAge(); got != bargeInSpeakerVerdictMaxAgeDefault {
		t.Errorf("bargeInSpeakerVerdictMaxAge() = %v, want default %v", got, bargeInSpeakerVerdictMaxAgeDefault)
	}
}

// TestBargeInSpeakerVerdictWiring proves BargeInSpeakerVerdict and
// BargeInSpeakerVerdictMaxAge (wired from config barge_in_speakers /
// speaker.live.window_ms in voiceagent.New and cmd/root.go) both reach the
// interrupt controller through newInterruptController.
func TestBargeInSpeakerVerdictWiring(t *testing.T) {
	const configured = 750 * time.Millisecond
	verdict := func() SpeakerVerdict { return SpeakerVerdict{Enrolled: true} }
	p := &Pipeline{BargeInSpeakerVerdict: verdict, BargeInSpeakerVerdictMaxAge: configured}

	if got := p.bargeInSpeakerVerdictMaxAge(); got != configured {
		t.Errorf("bargeInSpeakerVerdictMaxAge() = %v, want configured %v", got, configured)
	}

	ctrl := p.newInterruptController()
	if ctrl.verdictMaxAge != configured {
		t.Errorf("newInterruptController() verdictMaxAge = %v, want configured %v", ctrl.verdictMaxAge, configured)
	}
	if ctrl.speakerVerdict == nil {
		t.Fatal("newInterruptController() speakerVerdict is nil, want the configured func")
	}
	if got := ctrl.speakerVerdict(); !got.Enrolled {
		t.Error("newInterruptController() speakerVerdict did not thread through Pipeline.BargeInSpeakerVerdict")
	}
}

// TestInterruptControllerAnyoneModeHasNilSpeakerVerdict proves
// newInterruptController leaves speakerVerdict nil when
// Pipeline.BargeInSpeakerVerdict was never wired (barge_in_speakers=anyone,
// the default) — allowsInterrupt's nil check is what "never consulted" means.
func TestInterruptControllerAnyoneModeHasNilSpeakerVerdict(t *testing.T) {
	p := &Pipeline{}
	ctrl := p.newInterruptController()
	if ctrl.speakerVerdict != nil {
		t.Error("newInterruptController() speakerVerdict is non-nil with no Pipeline.BargeInSpeakerVerdict wired")
	}
	if !ctrl.allowsInterrupt() {
		t.Error("allowsInterrupt() = false with a nil speakerVerdict, want true (anyone mode is never blocked)")
	}
}

func TestInterruptControllerUnsubscribesOnCancel(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(context.Background())
	_ = c.watch(ctx, armedNow())

	waitForCond(t, func() bool { return capture.subCount() == 1 }, time.Second,
		"controller did not subscribe to capture")

	cancel()

	waitForCond(t, func() bool { return capture.subCount() == 0 }, time.Second,
		"controller did not unsubscribe from capture after cancellation")
	if vad.clearedCount() == 0 {
		t.Fatal("controller did not clear the VAD on exit")
	}
}
