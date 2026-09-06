package stt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/endpoint"
)

// fakeStreamingRec is a scriptable streamingRecognizer.
type fakeStreamingRec struct {
	partials      []string // one per Accept (clamped to the last entry)
	accepts       int
	endpointAfter int      // IsEndpoint() is true once accepts >= this (0 = never)
	finals        []string // consumed in order, one per Finalize() call
	finalIdx      int
	resets        int
	resetErr      error
}

func (f *fakeStreamingRec) Accept([]float32) { f.accepts++ }

func (f *fakeStreamingRec) Partial() string {
	i := f.accepts - 1
	if i < 0 {
		return ""
	}
	if i < len(f.partials) {
		return f.partials[i]
	}
	if len(f.partials) > 0 {
		return f.partials[len(f.partials)-1]
	}
	return ""
}

func (f *fakeStreamingRec) IsEndpoint() bool {
	return f.endpointAfter > 0 && f.accepts >= f.endpointAfter
}

func (f *fakeStreamingRec) Finalize() string {
	if f.finalIdx < len(f.finals) {
		v := f.finals[f.finalIdx]
		f.finalIdx++
		return v
	}
	if len(f.finals) > 0 {
		return f.finals[len(f.finals)-1]
	}
	return ""
}

func (f *fakeStreamingRec) Reset() error {
	f.resets++
	return f.resetErr
}

func runStreaming(ctx context.Context, deps streamingLoopDeps) []Event {
	events := make(chan Event, 128)
	go func() {
		runStreamingLoop(ctx, deps, events)
		close(events)
	}()
	var got []Event
	for e := range events {
		got = append(got, e)
	}
	return got
}

func countPartials(events []Event) int {
	n := 0
	for _, e := range events {
		if _, ok := e.(PartialTranscript); ok {
			n++
		}
	}
	return n
}

func TestStreamingLoopEOFFinalizesWithPartials(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"hello", "hello samantha", "hello samantha"}, finals: []string{"hello samantha"}},
		policy: endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	if countPartials(got) < 1 {
		t.Errorf("partials emitted = %d, want >= 1", countPartials(got))
	}
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "hello samantha" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "hello samantha")
	}
}

func TestStreamingLoopProviderEndpointFinalizes(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame(), speechFrame(), speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"a", "ab"}, endpointAfter: 2, finals: []string{"done"}},
		policy: endpoint.Policy{AllowProviderEnd: true, MinSpeech: 100 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "done" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "done")
	}
}

func TestStreamingLoopPolicyTrailingSilenceFinalizes(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{
			speechFrame(),         // 100ms speech
			make([]float32, 1600), // 100ms trailing silence
		}},
		seg:    &fakeSegmenter{speechSeq: []bool{true, false}},
		rec:    &fakeStreamingRec{finals: []string{"silence finalized"}},
		policy: endpoint.Policy{MinSpeech: 100 * time.Millisecond, MinSilence: 100 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "silence finalized" {
		t.Fatalf("FinalTranscript.Text = %q, want silence finalized", final.Text)
	}
}

func TestStreamingLoopNoSpeechTimesOut(t *testing.T) {
	deps := streamingLoopDeps{
		frames: noFrames{},
		seg:    &fakeSegmenter{speech: false},
		rec:    &fakeStreamingRec{},
		policy: endpoint.Policy{StartTimeout: time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout", terminal(got))
	}
}

// TestStreamingLoopSpuriousPartialResetsAndTimesOut is the TooShort-livelock
// regression guard: a non-empty recognizer partial with no VAD speech (noise or
// echo tail) marks speech, after which the start timeout can never fire. The
// loop must handle the policy's TooShort by resetting to a fresh listening
// window so the timeout recovers. Pre-fix this listened forever.
func TestStreamingLoopSilentEOFGhostFinalizeIsNotATurn(t *testing.T) {
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600), make([]float32, 1600)}},
		seg:    &fakeSegmenter{speech: true, flushSeg: make([]float32, minSpeechSamples)},
		rec:    &fakeStreamingRec{finals: []string{"Yeah."}},
		policy: endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond, StartTimeout: time.Second},
	}

	got := runStreaming(context.Background(), deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal = %T, want Timeout (silent EOF must not finalize as a turn)", terminal(got))
	}
	for _, e := range got {
		if ft, ok := e.(FinalTranscript); ok {
			t.Fatalf("emitted FinalTranscript on silent EOF: %q", ft.Text)
		}
	}
}

// A long stream cannot be buffered to be measured at EOF, so the audible
// verdict is tracked incrementally. Speech at the start of a long capture must
// still finalize once the audio behind it has been discarded.
func TestStreamingLoopEOFFinalizesSpeechHeardLongBefore(t *testing.T) {
	chunks := [][]float32{constFrame(1600, 0.08)}
	for i := 0; i < 400; i++ { // 40s of silence after the word
		chunks = append(chunks, make([]float32, 1600))
	}
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: chunks},
		// Silero never marked it live; only the EOF flush yields the segment.
		seg:    &fakeSegmenter{flushSeg: make([]float32, minSpeechSamples)},
		rec:    &fakeStreamingRec{finals: []string{"Hello there."}},
		policy: endpoint.Policy{FinalizeOnEOF: true, MinSpeech: 200 * time.Millisecond, StartTimeout: 30 * time.Second},
	}

	got := runStreaming(context.Background(), deps)
	ft, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal = %T, want FinalTranscript (early speech must survive a long silence)", terminal(got))
	}
	if ft.Text != "Hello there." {
		t.Fatalf("final = %q, want %q", ft.Text, "Hello there.")
	}
}

func TestStreamingLoopYeahPartialOnSilenceIsNotATurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deps := streamingLoopDeps{
		frames: busyFrames{},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"yeah"}, finals: []string{"Yeah."}},
		policy: endpoint.Policy{StartTimeout: 150 * time.Millisecond, MinSpeech: 50 * time.Millisecond},
	}

	got := runStreaming(ctx, deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal = %T, want Timeout (decoder ghost on silence is not a turn)", terminal(got))
	}
	for _, e := range got {
		if ft, ok := e.(FinalTranscript); ok {
			t.Fatalf("emitted FinalTranscript on silence: %q", ft.Text)
		}
	}
}

func TestStreamingLoopSpuriousPartialDoesNotStartATurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rec := &fakeStreamingRec{partials: []string{"x", ""}}
	deps := streamingLoopDeps{
		frames: busyFrames{},
		seg:    &fakeSegmenter{speech: false},
		rec:    rec,
		policy: endpoint.Policy{
			MinSpeech:    200 * time.Millisecond,
			MinSilence:   100 * time.Millisecond,
			StartTimeout: 150 * time.Millisecond,
		},
	}

	got := runStreaming(ctx, deps)
	if _, ok := terminal(got).(Timeout); !ok {
		t.Fatalf("terminal event = %T, want Timeout (ghost partial must not mark speech)", terminal(got))
	}
	for _, e := range got {
		if ft, ok := e.(FinalTranscript); ok {
			t.Fatalf("emitted FinalTranscript from a ghost partial: %q", ft.Text)
		}
	}
}

// TestStreamingLoopWedgedSourceMidSpeechStillEnds mirrors the offline wedge
// guard: speech marked, then the source stalls — the utterance cap must still
// finalize through the recognizer instead of polling forever.
func TestStreamingLoopWedgedSourceMidSpeechStillEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deps := streamingLoopDeps{
		frames: &wedgedFrames{chunks: [][]float32{speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true},
		rec:    &fakeStreamingRec{partials: []string{"partial words"}, finals: []string{"partial words"}},
		policy: endpoint.Policy{MinSpeech: 50 * time.Millisecond, MaxUtterance: 100 * time.Millisecond},
	}

	got := runStreaming(ctx, deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript (utterance cap must fire while wedged)", terminal(got))
	}
	if final.Text != "partial words" {
		t.Errorf("FinalTranscript.Text = %q", final.Text)
	}
}

func TestStreamingLoopCancellationFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{make([]float32, 1600)}},
		seg:    &fakeSegmenter{},
		rec:    &fakeStreamingRec{},
		policy: endpoint.Policy{},
	}

	got := runStreaming(ctx, deps)
	fail, ok := terminal(got).(Failure)
	if !ok {
		t.Fatalf("terminal event = %T, want Failure", terminal(got))
	}
	if !errors.Is(fail.Err, context.Canceled) {
		t.Errorf("Failure.Err = %v, want context.Canceled", fail.Err)
	}
}

func TestStreamingLoopEmptyFinalizeResetsThenRecovers(t *testing.T) {
	rec := &fakeStreamingRec{
		partials:      []string{"x"},
		endpointAfter: 1,
		finals:        []string{"", "recovered"}, // first finalize empty -> reset, second succeeds
	}
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame(), speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true},
		rec:    rec,
		policy: endpoint.Policy{AllowProviderEnd: true, MinSpeech: 50 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	final, ok := terminal(got).(FinalTranscript)
	if !ok {
		t.Fatalf("terminal event = %T, want FinalTranscript", terminal(got))
	}
	if final.Text != "recovered" {
		t.Errorf("FinalTranscript.Text = %q, want %q", final.Text, "recovered")
	}
	if rec.resets != 1 {
		t.Errorf("recognizer resets = %d, want 1", rec.resets)
	}
}

func TestStreamingLoopResetFailureEmitsFailure(t *testing.T) {
	resetErr := errors.New("new stream failed")
	rec := &fakeStreamingRec{
		partials:      []string{"x"},
		endpointAfter: 1,
		finals:        []string{""},
		resetErr:      resetErr,
	}
	deps := streamingLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame(), speechFrame()}},
		seg:    &fakeSegmenter{speech: true},
		rec:    rec,
		policy: endpoint.Policy{AllowProviderEnd: true, MinSpeech: 50 * time.Millisecond},
	}

	got := runStreaming(context.Background(), deps)
	fail, ok := terminal(got).(Failure)
	if !ok {
		t.Fatalf("terminal event = %T, want Failure", terminal(got))
	}
	if !errors.Is(fail.Err, resetErr) {
		t.Fatalf("Failure.Err = %v, want %v", fail.Err, resetErr)
	}
}
