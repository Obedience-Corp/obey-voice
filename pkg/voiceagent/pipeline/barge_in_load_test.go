package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// TestBargeInResponsiveUnderSynthAndQueueLoad proves barge-in interrupts a turn
// while synthesis work is in flight, the playback queue is full, and the model
// is still streaming. If the scheduler were blocked (e.g. a wedged queue send),
// the turn would hang past the deadline and this test would fail. It uses only
// fakes — no real microphone or audio hardware.
func TestBargeInResponsiveUnderSynthAndQueueLoad(t *testing.T) {
	bus := events.NewBus()
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	// Slow synthesis (TTS work in flight), long playbacks (queue saturates and
	// stays full), and more sentences than the queue depth (model keeps
	// streaming) — load on every stage at once.
	player := newFakePlayer(5 * time.Second)
	defer player.Close()
	capture := newFakeCapture()
	vad := &fakeVAD{}

	p := &Pipeline{
		STT:   &fakeSTT{text: "hello"},
		Brain: &fakeBrain{chunks: []string{"One. Two. Three. Four. Five. Six. Seven. Eight. Nine. Ten. Eleven. Twelve. Thirteen."}},
		// Thirteen sentences yields seven SEGMENTS: the turn loop speaks the
		// opening alone and batches two thereafter. Seven sentences used to mean
		// seven segments; leaving it there would have halved the load this test
		// puts on the queue without failing.
		TTS:        &fakeTTS{delay: 30 * time.Millisecond},
		Player:     player,
		Capture:    capture,
		VAD:        &fakeVAD{},
		BargeInVAD: vad,
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	// Playback has started: synthesis ran, the queue is filling, the loop is
	// backpressured waiting on the slow player.
	select {
	case <-player.StartedSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not start under load")
	}

	// Deliver sustained speech after the arm delay.
	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(60 * time.Millisecond)
	}

	// The turn must interrupt promptly; a scheduler blockage would hang here.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("barge-in did not interrupt under synth+playback load (scheduler blocked?)")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !response.Interrupted {
		t.Fatal("turn did not reach the interrupted terminal state")
	}

	// Player stop must have been requested, which also resolves the queued
	// playbacks (canceled-work cleanup); the turn draining proves the watchers
	// and streams unwound.
	if player.StopCount() == 0 {
		t.Fatal("playback stop was not requested on barge-in")
	}
	if capture.ResetCount() != 1 {
		t.Fatalf("capture.Reset() count = %d, want 1 (listen drain only)", capture.ResetCount())
	}
	if !p.keepCapture.Load() {
		t.Fatal("barge-in should preserve the capture buffer for the next listen")
	}
}

// TestRunTurnBargeInDuringPlaybackPause is the B3 end-to-end regression: user
// speech that happens entirely inside the queue-drain gap between two spoken
// sentences must interrupt the turn. Before F3.1/F3.2 the watcher disarmed the
// moment IsPlaying() went false and re-imposed the arm delay on every sentence,
// so pause speech was structurally ignored (WI-dc9e33 B3).
func TestRunTurnBargeInDuringPlaybackPause(t *testing.T) {
	bus := events.NewBus()
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	// Synthesis slower than playback opens a real IsPlaying()==false gap
	// between sentence one ending and sentence two starting. The first
	// playback outlasts the arm delay, so the turn is armed when the gap
	// opens; the gap comfortably outlasts the pause tail guard.
	player := newFakePlayer(800 * time.Millisecond)
	defer player.Close()
	capture := newFakeCapture()

	p := &Pipeline{
		STT:        &fakeSTT{text: "hello"},
		Brain:      &fakeBrain{chunks: []string{"One. Two."}},
		TTS:        &fakeTTS{delay: 1500 * time.Millisecond},
		Player:     player,
		Capture:    capture,
		VAD:        &fakeVAD{},
		BargeInVAD: &fakeVAD{},
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case <-player.StartedSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("first playback never started")
	}

	// The user talks into the pauses: speech chunks flow only while nothing is
	// playing, never overlapping Obey Voice's own audio.
	stopSpeaking := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSpeaking:
				return
			case <-ticker.C:
				if !player.IsPlaying() {
					capture.Publish([]float32{0.9, 0.9, 0.9})
				}
			}
		}
	}()
	defer close(stopSpeaking)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not interrupt on speech during the playback pause")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !response.Interrupted {
		t.Fatal("pause speech did not interrupt the turn")
	}
	if player.StopCount() == 0 {
		t.Fatal("playback stop was not requested on pause barge-in")
	}
	if !p.keepCapture.Load() {
		t.Fatal("pause barge-in should preserve the capture buffer for the next listen")
	}
}

// TestBargeInEmitsSincePlaybackStart is the S2 contextual-bookkeeping
// regression: a mic barge-in must tell downstream consumers how far into TTS
// playback the interrupt landed, on both events it produces.
func TestBargeInEmitsSincePlaybackStart(t *testing.T) {
	bus := events.NewBus()
	var (
		speakingInterrupted events.SpeakingInterrupted
		turnInterrupted     events.TurnInterrupted
	)
	turnInterruptedSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.SpeakingInterrupted) { speakingInterrupted = e })
	events.Subscribe(bus, func(e events.TurnInterrupted) {
		turnInterrupted = e
		select {
		case turnInterruptedSeen <- struct{}{}:
		default:
		}
	})

	player := newFakePlayer(5 * time.Second)
	defer player.Close()
	capture := newFakeCapture()

	p := &Pipeline{
		STT:        &fakeSTT{text: "hello"},
		Brain:      &fakeBrain{chunks: []string{"One. Two. Three."}},
		TTS:        &fakeTTS{delay: 30 * time.Millisecond},
		Player:     player,
		Capture:    capture,
		VAD:        &fakeVAD{},
		BargeInVAD: &fakeVAD{},
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case <-player.StartedSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not start")
	}

	// Wait past the arm delay, plus a margin, before tripping barge-in — the
	// recorded delta should reflect roughly this much time into playback.
	wait := bargeInArmDelay + 150*time.Millisecond
	time.Sleep(wait)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-turnInterruptedSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("did not observe TurnInterrupted")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunTurn did not return after barge-in")
	}

	if speakingInterrupted.SincePlaybackStart <= 0 {
		t.Fatalf("SpeakingInterrupted.SincePlaybackStart = %v, want > 0", speakingInterrupted.SincePlaybackStart)
	}
	if turnInterrupted.SincePlaybackStart != speakingInterrupted.SincePlaybackStart {
		t.Fatalf("TurnInterrupted.SincePlaybackStart = %v, want %v (same interrupt moment)",
			turnInterrupted.SincePlaybackStart, speakingInterrupted.SincePlaybackStart)
	}
	// The controller cannot trip before armAt elapses, so the recorded delta
	// can never be shorter than the arm delay.
	if speakingInterrupted.SincePlaybackStart < bargeInArmDelay {
		t.Fatalf("SincePlaybackStart = %v, want >= arm delay %v", speakingInterrupted.SincePlaybackStart, bargeInArmDelay)
	}
	// Sanity ceiling: this test waited ~wait plus a handful of 20ms chunks, not
	// anywhere near the fake player's 5s playback duration.
	if upper := wait + 2*time.Second; speakingInterrupted.SincePlaybackStart > upper {
		t.Fatalf("SincePlaybackStart = %v, want under %v (sanity upper bound)", speakingInterrupted.SincePlaybackStart, upper)
	}
}
