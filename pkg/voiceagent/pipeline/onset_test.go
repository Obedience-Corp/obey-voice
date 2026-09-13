package pipeline

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
)

type bufferedCapture struct {
	*fakeCapture
	ring *audio.RingBuffer
}

func (c *bufferedCapture) Reset() { c.fakeCapture.Reset(); c.ring.Clear() }
func (c *bufferedCapture) ResetKeeping(d time.Duration) {
	c.ring.KeepLast(int(d.Seconds() * audio.SampleRate))
}
func newBufferedCapture() *bufferedCapture {
	return &bufferedCapture{newFakeCapture(), audio.NewRingBuffer(audio.SampleRate * 30)}
}

func TestPreparedListenKeepsOnsetAndSpeechArrivingDuringAdmission(t *testing.T) {
	capture := newBufferedCapture()
	p := &Pipeline{Capture: capture, STT: &fakeSTT{}, CapturePreRoll: 300 * time.Millisecond}
	old := make([]float32, audio.SampleRate)
	for i := range old {
		old[i] = 1
	}
	onset := make([]float32, audio.SampleRate*3/10)
	for i := range onset {
		onset[i] = 2
	}
	capture.ring.Write(old)
	capture.ring.Write(onset)
	p.prepareNextListen()
	if p.HasCapturedInput() {
		t.Fatal("prepared silence blocked agent handoff")
	}
	// The user's remaining speech arrives while the persona classifier runs.
	rest := make([]float32, audio.SampleRate)
	for i := range rest {
		rest[i] = 3
	}
	capture.ring.Write(rest)
	session, generation, err := p.startCaptureSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer p.finishCaptureSession(session, generation, false)
	want := append(append([]float32(nil), onset...), rest...)
	if got := capture.ring.Read(len(want)); !reflect.DeepEqual(got, want) {
		t.Fatal("old output replayed or leading speech was erased")
	}
	if p.preparedCapture.Load() {
		t.Fatal("prepared scope was not consumed")
	}
}

func TestPreparedCaptureClearedOnExplicitCloseAndCanceledListen(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		capture := newBufferedCapture()
		p := &Pipeline{Capture: capture, STT: &fakeSTT{}}
		capture.ring.Write(make([]float32, 1000))
		p.prepareNextListen()
		if cancelled {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, _, _ = p.startCaptureSession(ctx)
		} else {
			p.closeCompletionCapture()
		}
		if p.preparedCapture.Load() || capture.ring.Available() != 0 {
			t.Fatal("retired capture scope survived")
		}
	}
}

func TestNewSpeechCancelsHandoffClassifierAndRetainsCapture(t *testing.T) {
	capture := newFakeCapture()
	p := &Pipeline{Capture: capture, BargeInVAD: &fakeVAD{}, Player: newFakePlayer(time.Millisecond)}
	started := make(chan struct{})
	p.RouteHandoff = func(ctx context.Context, _ brain.Turn, _ []string) (ResponseAdmission, bool, error) {
		close(started)
		<-ctx.Done()
		return ResponseAdmission{}, false, ctx.Err()
	}
	state := &completionState{voice: true, ok: true, turn: brain.Turn{Role: "assistant", Content: "Santa?", PersonaID: "jarvis"}}
	done := make(chan error, 1)
	go func() {
		_, accepted, err := p.nextHandoff(t.Context(), state, []string{"jarvis"})
		if accepted {
			done <- context.DeadlineExceeded
			return
		}
		done <- err
	}()
	<-started
	for range bargeInMinSpeechChunks + 1 {
		capture.Publish([]float32{.9, .9, .9})
		time.Sleep(2 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new speech did not cancel classifier")
	}
	if !p.keepCapture.Load() || capture.subCount() != 0 {
		t.Fatal("speech lost or VAD watcher not joined")
	}
}

func TestSilentHandoffClassificationDoesNotInventUserSpeech(t *testing.T) {
	capture := newFakeCapture()
	p := &Pipeline{Capture: capture, BargeInVAD: &fakeVAD{}, Player: newFakePlayer(time.Millisecond)}
	p.RouteHandoff = func(context.Context, brain.Turn, []string) (ResponseAdmission, bool, error) {
		capture.Publish([]float32{0, 0, 0})
		return admission("santa"), true, nil
	}
	_, accepted, err := p.nextHandoff(t.Context(), &completionState{voice: true, ok: true}, nil)
	if err != nil || !accepted || p.HasCapturedInput() || capture.subCount() != 0 {
		t.Fatalf("accepted=%v err=%v captured=%v", accepted, err, p.HasCapturedInput())
	}
}
