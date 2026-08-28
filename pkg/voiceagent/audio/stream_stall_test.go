package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPumpSegmentFailsStalledStream pins the failure a silent engine produces.
// Qwen publishes its sample rate on the first PCM chunk, which for a
// whole-utterance worker only arrives when synthesis finishes, so a worker that
// is too slow parks pumpSegment in WaitReady. Without a budget that wait is
// unbounded and the pipeline never learns it should switch voices.
func TestPumpSegmentFailsStalledStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := NewPCMStream(ctx)
	stream.SetStallBudget(30 * time.Millisecond)

	segment := newPlaybackSegment()
	p := &Player{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.pumpSegment(ctx, segment, stream)
	}()

	select {
	case result := <-segment.playback().Done():
		if !errors.Is(result.Err, ErrStreamStalled) {
			t.Fatalf("result.Err = %v, want ErrStreamStalled so the pipeline can fail over to the fallback voice", result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pumpSegment never failed a stream that produced no audio")
	}
	<-done
}

// TestPumpSegmentStallBudgetIsSilenceNotDuration is the constraint that keeps
// this compatible with streamed chunk playback: an engine slower than realtime
// that is still delivering audio must keep its segment. Only a stream that goes
// quiet for a whole budget is a stall.
func TestPumpSegmentStallBudgetIsSilenceNotDuration(t *testing.T) {
	const budget = 40 * time.Millisecond

	silence := newSilenceTimer(budget)
	defer silence.stop()

	// Eight batches at half the budget: total elapsed far exceeds the budget,
	// but no single gap does.
	for range 8 {
		time.Sleep(budget / 2)
		select {
		case <-silence.C():
			t.Fatal("silence timer fired while audio was still arriving; a slow-but-streaming engine would be cut off mid-utterance")
		default:
		}
		silence.reset()
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
	silence := newSilenceTimer(0)
	defer silence.stop()

	if silence.C() != nil {
		t.Fatal("silence timer channel is non-nil for a zero budget; a select on it would not block")
	}
	silence.reset() // must not panic
}
