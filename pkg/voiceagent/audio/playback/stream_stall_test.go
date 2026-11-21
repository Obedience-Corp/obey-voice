package playback

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// Some providers announce their sample rate when synthesis starts. Others
// publish it along with their first frames. Both can stall before any PCM.
func TestPumpSegmentFailsStalledStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate int
	}{
		{"waiting for sample rate", 0},
		{"rate known but no frames", 24000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stream := audio.NewPCMStream(t.Context())
				stream.SetStallBudgets(time.Second, 50*time.Millisecond)
				if tc.rate > 0 {
					if err := stream.SetSampleRate(tc.rate); err != nil {
						t.Fatal(err)
					}
				}
				defer stream.Close()
				p := NewPlayer()
				defer p.Close()
				p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
				start := time.Now()
				playCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				_, err := p.PlayStream(playCtx, stream)
				if !errors.Is(err, audio.ErrStreamStalled) || time.Since(start) != time.Second {
					t.Fatalf("expected startup stall at 1s, got %v at %v", err, time.Since(start))
				}
			})
		})
	}
}

// TestPumpSegmentStallBudgetIsSilenceNotDuration is the constraint that keeps
// this compatible with streamed chunk playback: an engine slower than realtime
// that is still delivering audio must keep its segment. Only a stream that goes
// quiet for a whole budget is a stall.
func TestPumpSegmentStallBudgetIsSilenceNotDuration(t *testing.T) {
	const budget = 40 * time.Millisecond

	stream := audio.NewPCMStream(t.Context())
	stream.SetStallBudget(budget)
	silence := testStallTimer(t, stream)

	// Eight batches at half the budget: total elapsed far exceeds the budget,
	// but no single gap does.
	for range 8 {
		time.Sleep(budget / 2)
		select {
		case <-silence.C():
			t.Fatal("silence timer fired while audio was still arriving; a slow-but-streaming engine would be cut off mid-utterance")
		default:
		}
		silence.ReceivedFrames()
	}

	select {
	case <-silence.C():
	case <-time.After(2 * time.Second):
		t.Fatal("silence timer never fired after audio stopped arriving")
	}
}

// TestSilenceTimerDisabledWithoutBudget keeps the unbounded default: a provider
// that publishes no grace must not gain a deadline it never had.
func TestSilenceTimerDisabledWithoutBudget(t *testing.T) {
	silence := testStallTimer(t, audio.NewPCMStream(t.Context()))

	if silence.C() != nil {
		t.Fatal("silence timer channel is non-nil for a zero budget; a select on it would not block")
	}
	silence.ReceivedFrames() // must not panic
}

func testStallTimer(t *testing.T, stream *audio.PCMStream) *audio.StreamStallTimer {
	t.Helper()
	timer := stream.NewStallTimer()
	t.Cleanup(timer.Stop)
	return timer
}

// Exercise the normal player entry point with a software device. Phonon
// publishes its sample rate immediately, even while Ollama loads its runner.
func TestColdStartupThenFrameGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		p := NewPlayer()
		defer p.Close()
		p.testOpen = func(rate int) (nativeOutput, int, int, error) { return &fakeOutput{}, rate, 2, nil }
		stream := audio.NewPCMStream(ctx)
		stream.SetIncremental(true)
		stream.SetStallBudgets(30*time.Second, 5*time.Second)
		if err := stream.SetSampleRate(24000); err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		audible := make(chan struct{}, 1)
		go func() {
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			out := make([]byte, 480*2*2) // 20 ms of stereo PCM16 at 24 kHz.
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-tick.C:
					p.observeCallback(now)
					p.onData(out, nil, 480)
					if slices.ContainsFunc(out, func(b byte) bool { return b != 0 }) {
						select {
						case audible <- struct{}{}:
						default:
						}
					}
				}
			}
		}()
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(9110 * time.Millisecond):
			}
			if err := stream.Write(chunkSamples(0, 12000, 24000)); err != nil {
				t.Error(err)
			}
		}()
		start := time.Now()
		playback, err := p.PlayStream(ctx, stream)
		if err != nil {
			t.Fatalf("healthy cold start failed: %v", err)
		}
		if got := time.Since(start); got != 9110*time.Millisecond {
			t.Fatalf("first audio ready after %v", got)
		}
		select {
		case <-audible:
		case <-time.After(40 * time.Millisecond):
			t.Fatal("device received no audible PCM")
		}
		select {
		case result := <-playback.Done():
			if !errors.Is(result.Err, audio.ErrStreamStalled) || time.Since(start) != 14110*time.Millisecond {
				t.Fatalf("next batch timed out after %v: %v", time.Since(start), result.Err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("stall did not terminate playback")
		}
	})
}
