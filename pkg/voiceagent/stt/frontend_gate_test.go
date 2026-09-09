package stt

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// The meeting front-end is AGC only, so these exercise the gate and the
// not-silence floor against what capture actually hands the loops: samples
// already multiplied by up to 8.

func TestListenGainFarTalkerClearsTheGate(t *testing.T) {
	t.Parallel()
	front := audio.NewListenGain()
	var gate noiseGate

	room := func() []float32 { return constFrame(1600, 0.001) }
	for i := 0; i < 20; i++ {
		gate.minRMS = speechFloor(front.InputGain())
		gate.decide(front.ProcessCapture(room()), false)
	}

	off := 0
	for i := 0; i < 5; i++ {
		raw := modulatedFrame(1600, off, 0.01, 0.03)
		off += 1600
		gate.minRMS = speechFloor(front.InputGain())
		d := gate.decide(front.ProcessCapture(raw), true)
		if d.drop {
			t.Fatalf("far-talker frame %d dropped through the real meeting front-end", i)
		}
		if !d.voice {
			t.Fatalf("far-talker frame %d: the talker this path exists to hear must count as voice", i)
		}
	}
}

func TestListenGainRoomToneIsNotAudibleButAFarTalkerIs(t *testing.T) {
	t.Parallel()
	front := audio.NewListenGain()

	// 0.001 raw room tone reads 0.008 after 8x AGC, four times the pre-gain
	// floor. Against a scaled floor it is still nobody talking.
	var tone []float32
	for i := 0; i < 20; i++ {
		tone = front.ProcessCapture(constFrame(4800, 0.001))
	}
	floor := speechFloor(front.InputGain())
	if audibleSpeechAbove(tone, floor) {
		t.Fatalf("AGC'd room tone (rms %v, gain %v, floor %v) must not guard-pass as a talker",
			frameRMS(tone), front.InputGain(), floor)
	}
	// Only the gain made it look like a talker: at the microphone it is silence.
	if audibleSpeech(constFrame(4800, 0.001)) {
		t.Fatal("0.001 raw room tone is below the microphone floor")
	}

	// A talker across the table through the same front-end is.
	off := 0
	var talk []float32
	for i := 0; i < 8; i++ {
		talk = front.ProcessCapture(modulatedFrame(4800, off, 0.002, 0.006))
		off += 4800
	}
	floor = speechFloor(front.InputGain())
	if !audibleSpeechAbove(talk, floor) {
		t.Fatalf("far talker (rms %v, gain %v, floor %v) must clear the guard",
			frameRMS(talk), front.InputGain(), floor)
	}
}

func TestSpeechFloorScalesWithAppliedGain(t *testing.T) {
	t.Parallel()
	if got := speechFloor(1); got != minSpeechRMS {
		t.Fatalf("unamplified floor = %v, want %v", got, minSpeechRMS)
	}
	if got := speechFloor(0.5); got != minSpeechRMS {
		t.Fatalf("floor under a cutting AGC = %v, want the microphone floor %v", got, minSpeechRMS)
	}
	if got := speechFloor(8); got != minSpeechRMS*8 {
		t.Fatalf("floor at 8x = %v, want %v", got, minSpeechRMS*8)
	}
}

func TestInputGainReportsUnamplifiedSourcesAsOne(t *testing.T) {
	t.Parallel()
	if got := inputGain(noFrames{}); got != 1 {
		t.Fatalf("a source that does not amplify reports gain %v, want 1", got)
	}
	capture := audio.NewCapture()
	if got := inputGain(capture); got != 1 {
		t.Fatalf("capture with no front-end reports gain %v, want 1", got)
	}
	capture.SetFrontend(audio.NewListenGain())
	if got := inputGain(capture); got != 1 {
		t.Fatalf("an idle AGC reports gain %v, want 1 before it has seen audio", got)
	}
}
