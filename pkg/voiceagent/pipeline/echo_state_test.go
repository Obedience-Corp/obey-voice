package pipeline

import (
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type echoResetDetector struct{ clears atomic.Int32 }

func (d *echoResetDetector) AcceptWaveform([]float32) {}
func (d *echoResetDetector) IsSpeech() bool           { return false }
func (d *echoResetDetector) IsSpeechDetected() bool   { return false }
func (d *echoResetDetector) Clear()                   { d.clears.Add(1) }

type echoCaptureSession struct{}

func (echoCaptureSession) Events() <-chan stt.Event { return make(chan stt.Event) }
func (echoCaptureSession) Close() error             { return nil }

// A yielded STT owner still owns the VAD and its segment-offset/pre-roll
// state. Resetting the shared detector from the completion response path would
// reset native offsets without resetting that live owner.
func TestResetEchoStateLeavesYieldedCaptureOwnerUntouched(t *testing.T) {
	captureVAD := &echoResetDetector{}
	bargeVAD := &echoResetDetector{}
	p := &Pipeline{VAD: captureVAD, BargeInVAD: bargeVAD, captureSession: echoCaptureSession{}, captureYielded: true}
	p.resetEchoState()
	if got := captureVAD.clears.Load(); got != 0 {
		t.Fatalf("yielded capture VAD reset count=%d, want 0", got)
	}
	if got := bargeVAD.clears.Load(); got != 1 {
		t.Fatalf("barge VAD reset count=%d, want 1", got)
	}
}

func TestResetEchoStateResetsWithoutYieldedCaptureOwner(t *testing.T) {
	captureVAD := &echoResetDetector{}
	bargeVAD := &echoResetDetector{}
	p := &Pipeline{VAD: captureVAD, BargeInVAD: bargeVAD}
	p.resetEchoState()
	if got := captureVAD.clears.Load() + bargeVAD.clears.Load(); got != 2 {
		t.Fatalf("reset count=%d, want 2", got)
	}
}

var _ stt.Session = echoCaptureSession{}
