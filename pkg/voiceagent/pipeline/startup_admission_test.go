package pipeline

import (
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type startupFakeCapture struct {
	raw, residual, post, replaced []float32
	ingested                      uint64
	rawReplaced                   bool
}

func (s *startupFakeCapture) Subscribe(int) (int, <-chan []float32) { return 0, nil }
func (s *startupFakeCapture) Unsubscribe(int)                       {}
func (s *startupFakeCapture) Reset()                                {}

func (s *startupFakeCapture) SnapshotStartupWindows(time.Duration) ([]float32, []float32) {
	return s.raw, s.residual
}

func (s *startupFakeCapture) Ingested() uint64 { return s.ingested }

func (s *startupFakeCapture) Newest(int) []float32 { return s.post }

func (s *startupFakeCapture) ReplaceUnread(samples []float32) {
	s.replaced = slices.Clone(samples)
}

func (s *startupFakeCapture) ReplaceUnreadWithRaw(time.Duration) {
	s.rawReplaced = true
}

func TestApplyStartupAdmissionUsesResidualWhenTailIsQuiet(t *testing.T) {
	residual := make([]float32, 4800)
	raw := make([]float32, 9600)
	for i := range residual {
		residual[i] = 0.04
	}
	for i := range raw {
		raw[i] = 0.03
	}
	post := make([]float32, 3200)
	for i := range post {
		post[i] = 0.002
	}
	cap := &startupFakeCapture{raw: raw, residual: residual, post: post, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: 0})
	p.applyStartupAdmission(t.Context())
	if !slices.Equal(cap.replaced, audio.PadStartupListen(audio.NormalizeSpeechRMS(residual))) {
		t.Fatal("quiet post-stop tail did not keep the NearEnd residual without the APM tail")
	}
}

func TestApplyStartupAdmissionKeepsRawWhenTailIsSpeech(t *testing.T) {
	residual := make([]float32, 4800)
	raw := make([]float32, 9600)
	for i := range residual {
		residual[i] = 0.04
	}
	for i := range raw {
		raw[i] = 0.03
	}
	post := make([]float32, 3200)
	for i := range post {
		post[i] = 0.08
	}
	cap := &startupFakeCapture{raw: raw, residual: residual, post: post, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: 0})
	p.applyStartupAdmission(t.Context())
	if cap.replaced != nil {
		t.Fatal("continuing speech replaced the raw keep window with the residual crop")
	}
}

func TestApplyStartupAdmissionKeepsRawWhenPostIsEmpty(t *testing.T) {
	residual := make([]float32, 4800)
	raw := make([]float32, 9600)
	for i := range residual {
		residual[i] = 0.04
	}
	for i := range raw {
		raw[i] = 0.03
	}
	cap := &startupFakeCapture{raw: raw, residual: residual, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: 0})
	p.applyStartupAdmission(t.Context())
	if cap.replaced != nil {
		t.Fatal("empty post-stop tail must keep the raw window, not the residual crop")
	}
}

func TestApplyStartupAdmissionIgnoresEarlyPostStopEcho(t *testing.T) {
	residual := make([]float32, 4800)
	raw := make([]float32, 9600)
	for i := range residual {
		residual[i] = 0.04
	}
	for i := range raw {
		raw[i] = 0.03
	}
	post := make([]float32, 16000)
	for i := range 6400 {
		post[i] = 0.08
	}
	for i := 6400; i < len(post); i++ {
		post[i] = 0.002
	}
	cap := &startupFakeCapture{raw: raw, residual: residual, post: post, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: 0})
	p.applyStartupAdmission(t.Context())
	want := audio.PadStartupListen(audio.NormalizeSpeechRMS(residual))
	if !slices.Equal(cap.replaced, want) {
		t.Fatal("echo immediately after stop kept the raw mix instead of the NearEnd crop")
	}
}

func TestApplyStartupAdmissionKeepsRawWhenNewestTailIsSpeech(t *testing.T) {
	residual := make([]float32, 4800)
	raw := make([]float32, 9600)
	for i := range residual {
		residual[i] = 0.04
	}
	for i := range raw {
		raw[i] = 0.03
	}
	post := make([]float32, 16000)
	for i := 9600; i < len(post); i++ {
		post[i] = 0.08
	}
	cap := &startupFakeCapture{raw: raw, residual: residual, post: post, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.startup.Store(&startupAdmission{residual: residual, ingestedAt: 0})
	p.applyStartupAdmission(t.Context())
	if cap.replaced != nil {
		t.Fatal("continuing speech in the newest tail overwrote the raw keep window")
	}
}

func TestApplyStartupAdmissionEchoOnlyDoesNotOverwrite(t *testing.T) {
	raw := make([]float32, 9600)
	for i := range raw {
		raw[i] = 0.03
	}
	post := make([]float32, 3200)
	for i := range post {
		post[i] = 0.002
	}
	cap := &startupFakeCapture{raw: raw, post: post, ingested: 20000}
	p := &Pipeline{Capture: cap}
	p.retainInterruptedCapture(interruptRequest{Source: interruptSourceEvidence{DuringArm: true}}, 600*time.Millisecond)
	p.applyStartupAdmission(t.Context())
	if cap.replaced != nil {
		t.Fatal("echo-only overwrite installed a Wait crop")
	}
}

func TestRetainInterruptedCaptureSnapshotsDuringArm(t *testing.T) {
	raw := make([]float32, 1600)
	residual := make([]float32, 4800)
	cap := &startupFakeCapture{raw: raw, residual: residual, ingested: 8000}
	p := &Pipeline{Capture: cap}
	p.retainInterruptedCapture(interruptRequest{Source: interruptSourceEvidence{DuringArm: true}}, 600*time.Millisecond)
	if !p.keepCapture.Load() {
		t.Fatal("DuringArm barge-in must keep capture")
	}
	adm := p.startup.Load()
	if adm == nil || !slices.Equal(adm.residual, residual) || adm.ingestedAt != 8000 {
		t.Fatalf("startup snapshot = %+v", adm)
	}
	if !cap.rawReplaced {
		t.Fatal("DuringArm retain must install the raw keep window immediately")
	}
}
