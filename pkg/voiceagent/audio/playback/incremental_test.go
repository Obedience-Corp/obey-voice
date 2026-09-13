package playback

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"math"
	"sync"
	"testing"
	"time"
)

// pending and underrunCount read segment state the way the device callback
// does. They live here rather than in player.go so production code carries no
// accessor that only a test calls.
func (s *playbackSegment) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

func (s *playbackSegment) underrunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.underruns
}

// drainSegment plays a segment out the way the device callback does, so its
// terminal result is delivered. A segment with audio still buffered is not
// finished, whatever the synthesizer has done — that is the player's contract,
// not a test detail.
func drainSegment(t *testing.T, s *playbackSegment) audio.PlaybackResult {
	t.Helper()
	out := make([]byte, 2*playbackPeriodFrames)
	for {
		_, finished := s.writeTo(out, playbackPeriodFrames)
		if finished {
			s.complete()
			break
		}
	}
	select {
	case result := <-s.playback().Done():
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("segment never delivered a terminal result")
		return audio.PlaybackResult{}
	}
}

// chunkSamples builds one chunk of a continuous sine so a boundary artefact
// shows up as a discontinuity rather than as plausible noise.
func chunkSamples(start, n, rate int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.Sin(2 * math.Pi * 220 * float64(start+i) / float64(rate)))
	}
	return out
}

// feedIncremental runs pumpIncremental against a stream fed one chunk at a
// time, returning once the pump has finished with the closed stream.
func feedIncremental(t *testing.T, ctx context.Context, segment *playbackSegment, inputRate, outputRate int, chunks [][]float32) {
	t.Helper()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(inputRate); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpIncremental(ctx, segment, stream, nil, inputRate, outputRate, 0)
	}()
	for _, chunk := range chunks {
		if err := stream.Write(chunk); err != nil {
			t.Errorf("Write: %v", err)
			break
		}
	}
	stream.Close()
	<-done
}

// The whole point of slice 5c: a segment fed incrementally is playable before
// the synthesizer has finished, which is what makes time-to-first-audio the
// first chunk instead of the whole utterance.
func TestIncrementalSegmentIsAudibleBeforeTheStreamCloses(t *testing.T) {
	const rate = 24000
	ctx := context.Background()
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	go pumpIncremental(ctx, segment, stream, nil, rate, rate, 0)

	// One chunk, comfortably more than the pre-roll, and then nothing.
	if err := stream.Write(chunkSamples(0, rate/2, rate)); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := segment.waitReady(waitCtx); err != nil {
		t.Fatalf("segment did not become audible from the first chunk: %v", err)
	}
	// The stream is still open: readiness came from the pre-roll, not the end.
	if segment.pending() == 0 {
		t.Fatal("segment reported ready with nothing buffered")
	}
	stream.Close()
}

// The buffered path must keep its old behaviour exactly: a whole-utterance
// engine gains nothing from partial playback and loses its underrun immunity.
func TestWholeUtteranceSegmentWaitsForTheStreamToClose(t *testing.T) {
	const rate = 24000
	ctx := context.Background()
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	go func() {
		// pumpSegment would open a device; the buffered loop under test is the
		// one finalizeSegment terminates, so drive it directly.
		var samples []float32
		segment.setReadyFrames(0)
		for frames := range stream.Frames() {
			samples = append(samples, frames...)
		}
		finalizeSegment(segment, nil, samples, rate, rate, stream.Err())
	}()

	if err := stream.Write(chunkSamples(0, rate/2, rate)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-segment.ready:
		t.Fatal("a buffered segment became audible before its stream closed")
	case <-time.After(50 * time.Millisecond):
	}
	stream.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := segment.waitReady(waitCtx); err != nil {
		t.Fatalf("waitReady after close: %v", err)
	}
}

// Chunk boundaries must not be audible. A per-chunk whole-buffer resample
// restarts its phase and clamps its tail every time, which puts a step at every
// join; the phase-preserving one has to reconstruct what a single resample of
// the concatenated signal would have produced.
func TestIncrementalResampleIsContinuousAcrossChunks(t *testing.T) {
	const inputRate, outputRate = 24000, 44100
	whole := chunkSamples(0, 4800, inputRate)
	want := resample(append([]float32(nil), whole...), inputRate, outputRate)

	var resampler streamResampler
	resampler.Configure(inputRate, outputRate)
	var got []float32
	var buf []float32
	for start := 0; start < len(whole); start += 512 {
		end := min(start+512, len(whole))
		buf = resampleChunk(&resampler, whole[start:end], buf, inputRate, outputRate)
		got = append(got, buf...)
	}

	// The chunked path holds back at most the final interpolation neighbour.
	if len(want)-len(got) > 2 || len(got) > len(want) {
		t.Fatalf("chunked resample produced %d samples, whole-buffer produced %d", len(got), len(want))
	}
	for i := range got {
		if diff := math.Abs(float64(got[i] - want[i])); diff > 1e-3 {
			t.Fatalf("sample %d differs by %g: chunk boundaries are audible", i, diff)
		}
	}
}

// A gap inside a sentence is the failure mode streaming introduces, and it has
// to be counted: dead-air accounting only sees silence *between* segments, so
// without this a stuttering reply and a clean one look identical.
func TestSegmentCountsUnderrunsOncePerGap(t *testing.T) {
	segment := newPlaybackSegment()
	out := make([]byte, 64)

	// Not audible yet: waiting in the queue is not an underrun.
	if _, _ = segment.writeTo(out, 8); segment.underrunCount() != 0 {
		t.Fatalf("underruns=%d before any audio was played", segment.underrunCount())
	}

	segment.append([]int16{1, 2, 3, 4})
	if written, _ := segment.writeTo(out, 4); written != 4 {
		t.Fatalf("writeTo wrote %d, want 4", written)
	}
	// Dry while the synthesizer is still producing: one gap, however many
	// device callbacks ask during it.
	for range 5 {
		segment.writeTo(out, 8)
	}
	if got := segment.underrunCount(); got != 1 {
		t.Fatalf("underruns=%d, want 1 for a single continuous gap", got)
	}

	segment.append([]int16{5, 6})
	segment.writeTo(out, 2)
	segment.writeTo(out, 2)
	if got := segment.underrunCount(); got != 2 {
		t.Fatalf("underruns=%d, want 2 after a second gap", got)
	}

	// Once input is done, running out is the end of the segment, not a gap.
	segment.finishInput(nil)
	segment.writeTo(out, 2)
	if got := segment.underrunCount(); got != 2 {
		t.Fatalf("underruns=%d, want 2: the end of a segment is not an underrun", got)
	}
	if result := <-segment.playback().Done(); result.Underruns != 2 {
		t.Fatalf("PlaybackResult.Underruns=%d, want 2", result.Underruns)
	}
}

// Barge-in discards the buffer; the callbacks that follow are not underruns.
func TestInterruptedSegmentReportsNoUnderruns(t *testing.T) {
	segment := newPlaybackSegment()
	out := make([]byte, 64)
	segment.append([]int16{1, 2})
	segment.writeTo(out, 2)
	segment.interrupt()
	segment.writeTo(out, 2)
	result := <-segment.playback().Done()
	if result.Underruns != 0 {
		t.Fatalf("Underruns=%d on an interrupted segment", result.Underruns)
	}
	if !result.SynthesisEndedAt.IsZero() {
		t.Error("an interrupted segment must not claim the synthesizer finished")
	}
}

// The realtime factor's numerator is when synthesis ended. A streamed segment
// is audible long before that, so the player has to stamp it — otherwise every
// streamed segment reports a fraction of its true cost.
func TestIncrementalSegmentStampsWhenSynthesisEnded(t *testing.T) {
	const rate = 24000
	segment := newPlaybackSegment()
	started := time.Now()
	feedIncremental(t, context.Background(), segment, rate, rate, [][]float32{
		chunkSamples(0, 2400, rate),
		chunkSamples(2400, 2400, rate),
	})
	result := drainSegment(t, segment)
	if result.SynthesisEndedAt.IsZero() {
		t.Fatal("SynthesisEndedAt is unset after the stream closed")
	}
	if result.SynthesisEndedAt.Before(started) {
		t.Fatalf("SynthesisEndedAt %v predates the run", result.SynthesisEndedAt)
	}
	// Duration is accumulated per chunk, never measured over a whole buffer.
	if want := 4800.0 / rate; math.Abs(result.AudioSeconds-want) > 1e-9 {
		t.Fatalf("AudioSeconds=%v, want %v summed across chunks", result.AudioSeconds, want)
	}
}

// A cancelled turn must end the segment, not leave the pump holding it.
func TestIncrementalPumpFailsTheSegmentOnCancel(t *testing.T) {
	const rate = 24000
	ctx, cancel := context.WithCancel(context.Background())
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpIncremental(ctx, segment, stream, nil, rate, rate, 0)
	}()
	if err := stream.Write(chunkSamples(0, 2400, rate)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpIncremental did not return after cancel")
	}
	result := <-segment.playback().Done()
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("result.Err=%v, want context.Canceled", result.Err)
	}
}

// A worker that keeps emitting chunks, however slowly, is not stalled. The
// budget bounds silence between batches; the whole-utterance path already
// works this way and the streamed one must not diverge.
func TestIncrementalPumpStallBudgetIsSilenceNotDuration(t *testing.T) {
	const rate = 24000
	ctx := context.Background()
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	stream.SetStallBudget(120 * time.Millisecond)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpIncremental(ctx, segment, stream, nil, rate, rate, stream.StallBudget())
	}()
	// Six batches at 40 ms apart run well past the 120 ms budget in total
	// while never being silent for it.
	for i := range 6 {
		time.Sleep(40 * time.Millisecond)
		if err := stream.Write(chunkSamples(i*240, 240, rate)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	stream.Close()
	<-done
	result := drainSegment(t, segment)
	if errors.Is(result.Err, audio.ErrStreamStalled) {
		t.Fatal("a slow but steadily streaming worker was judged stalled")
	}
	if result.Err != nil {
		t.Fatalf("result.Err=%v", result.Err)
	}
}

// Silence past the budget still fails the segment, so the pipeline can reach
// the fallback voice instead of holding a dead turn.
func TestIncrementalPumpFailsOnSilencePastTheBudget(t *testing.T) {
	const rate = 24000
	ctx := context.Background()
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpIncremental(ctx, segment, stream, nil, rate, rate, 50*time.Millisecond)
	}()
	if err := stream.Write(chunkSamples(0, 240, rate)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpIncremental never gave up on a silent worker")
	}
	result := <-segment.playback().Done()
	if !errors.Is(result.Err, audio.ErrStreamStalled) {
		t.Fatalf("result.Err=%v, want ErrStreamStalled", result.Err)
	}
	stream.Close()
}

// The device callback and the synth pump touch the same segment from different
// goroutines; -race has to have something to look at.
func TestIncrementalPumpRacesTheDeviceCallback(t *testing.T) {
	const rate = 24000
	ctx := context.Background()
	segment := newPlaybackSegment()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(rate); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pumpIncremental(ctx, segment, stream, nil, rate, rate, 0)
	}()
	stop := make(chan struct{})
	go func() {
		defer wg.Done()
		out := make([]byte, 2*playbackPeriodFrames)
		for {
			select {
			case <-stop:
				return
			default:
			}
			segment.writeTo(out, playbackPeriodFrames)
			_ = segment.underrunCount()
		}
	}()

	for i := range 40 {
		if err := stream.Write(chunkSamples(i*240, 240, rate)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	stream.Close()
	close(stop)
	wg.Wait()
}
