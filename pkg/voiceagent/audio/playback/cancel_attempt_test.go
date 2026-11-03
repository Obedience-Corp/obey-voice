package playback

import (
	"context"
	"encoding/binary"
	"runtime"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// A buffered producer has already exited when a filler is replaced. Its
// playback context must still interrupt queued/current PCM without revoking the
// answer admitted under the same output generation.
func TestCancelBufferedAttemptPreservesAnswerAdmission(t *testing.T) {
	for _, playing := range []bool{false, true} {
		name := "queued"
		if playing {
			name = "playing"
		}
		t.Run(name, func(t *testing.T) {
			p := NewPlayer()
			defer p.Close()
			p.SetVolume(1)
			p.testOpen = func(rate int) (nativeOutput, int, int, error) {
				return &fakeOutput{}, rate, 2, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			ctx = p.BindPlaybackContext(ctx)
			fillerCtx, stopFiller := context.WithCancel(ctx)
			defer stopFiller()
			filler, err := p.PlayStream(fillerCtx, bufferedAttempt(t, fillerCtx, []float32{-.5, -.5, -.5, -.5}))
			if err != nil {
				t.Fatal(err)
			}
			if playing {
				p.onData(make([]byte, 4), nil, 1)
				select {
				case <-filler.Started():
				default:
					t.Fatal("filler did not start")
				}
			}
			stopFiller()
			select {
			case result := <-filler.Done():
				if !result.Interrupted {
					t.Fatalf("canceled filler result: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal("buffered filler ignored per-attempt cancellation")
			}
			answer, err := p.PlayStream(ctx, bufferedAttempt(t, ctx, []float32{.25, .25}))
			if err != nil {
				t.Fatalf("filler cancellation revoked foreground answer: %v", err)
			}
			out := make([]byte, 8)
			p.onData(out, nil, 2)
			for i := 0; i < len(out); i += 2 {
				if sample := int16(binary.LittleEndian.Uint16(out[i:])); sample != 8191 {
					t.Fatalf("rendered sample %d = %d; want only answer PCM", i/2, sample)
				}
			}
			select {
			case result := <-answer.Done():
				if result.Interrupted || result.Err != nil {
					t.Fatalf("answer result: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal("answer did not complete")
			}
		})
	}
}

func bufferedAttempt(t *testing.T, ctx context.Context, samples []float32) *audio.PCMStream {
	t.Helper()
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	if err := stream.Write(samples); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	return stream
}

// A canceled streaming pump and the lifecycle watcher wake together. The pump
// may publish its terminal failure first, making both finishedCh and ctx.Done
// selectable. Regardless of which wins, cancellation must clear buffered PCM.
func TestCancelIncrementalAttemptClearsPCMWhenWorkerFinishes(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)
	p := NewPlayer()
	defer p.Close()
	p.SetVolume(1)
	p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = p.BindPlaybackContext(ctx)
	samples := make([]float32, preRollFrames(24000)+8)
	for i := range samples {
		samples[i] = -.5
	}
	// Publish pump completion before cancellation, with the watcher unable
	// to run between them. No production test seam is needed.
	attemptCtx, stop := context.WithCancel(ctx)
	stream := audio.NewPCMStream(attemptCtx)
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	stream.SetIncremental(true)
	if err := stream.Write(samples); err != nil {
		t.Fatal(err)
	}
	playback, err := p.PlayStream(attemptCtx, stream)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	// Leave the incremental producer open and render a prefix, ensuring both
	// buffered output and a still-running pump exist when cancellation arrives.
	p.onData(make([]byte, 4), nil, 1)
	p.mu.Lock()
	segment := p.current
	p.mu.Unlock()
	// Model the incremental pump winning the cancellation race before
	// the watcher is scheduled. GOMAXPROCS(1) keeps this immediate pair
	// together; both terminal and cancellation channels are now ready.
	segment.fail(context.Canceled)
	stop()
	select {
	case <-playback.Done():
	case <-ctx.Done():
		t.Fatal("canceled incremental attempt did not finish")
	}
	for {
		p.mu.Lock()
		workers := len(p.workers)
		p.mu.Unlock()
		if workers == 0 {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("canceled worker did not unregister")
		}
	}
	stream.Close()
	answer, err := p.PlayStream(ctx, bufferedAttempt(t, ctx, []float32{.25, .25}))
	if err != nil {
		t.Fatalf("cancellation revoked same-generation answer: %v", err)
	}
	out := make([]byte, 8)
	p.onData(out, nil, 2)
	for offset := 0; offset < len(out); offset += 2 {
		if sample := int16(binary.LittleEndian.Uint16(out[offset:])); sample != 8191 {
			t.Fatalf("canceled incremental PCM survived worker cleanup: %d", sample)
		}
	}
	select {
	case result := <-answer.Done():
		if result.Err != nil || result.Interrupted {
			t.Fatalf("answer failed: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("same-generation answer did not finish")
	}
}
