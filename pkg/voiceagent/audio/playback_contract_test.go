package audio

import (
	"errors"
	"testing"
)

func TestPlaybackWaitStartRetainsCompletedStartAndReceipt(t *testing.T) {
	for i := 0; i < 100; i++ {
		started := make(chan struct{})
		close(started)
		failure := errors.New("finished after audible start")
		done := make(chan PlaybackResult, 1)
		done <- PlaybackResult{AudioSeconds: 1, Err: failure, Interrupted: true}
		close(done)
		p := NewPlayback(started, done)
		didStart, terminal := p.WaitStart(nil)
		if !didStart {
			t.Fatal("completion hid an observed start")
		}
		if terminal == nil {
			result := <-p.Done()
			terminal = &result
		}
		if terminal.AudioSeconds != 1 || !errors.Is(terminal.Err, failure) || !terminal.Interrupted {
			t.Fatalf("terminal receipt lost: %+v", terminal)
		}
	}
}

func TestPlaybackWaitStartPreservesNoStartFailure(t *testing.T) {
	failure := errors.New("no device")
	done := make(chan PlaybackResult, 1)
	done <- PlaybackResult{Err: failure}
	close(done)
	p := NewPlayback(make(chan struct{}), done)
	started, terminal := p.WaitStart(nil)
	if started || terminal == nil || !errors.Is(terminal.Err, failure) {
		t.Fatalf("non-starting failure changed: started=%v terminal=%+v", started, terminal)
	}
}

func TestPlaybackWaitStartRespectsCanceledOwner(t *testing.T) {
	for _, alreadyStarted := range []bool{false, true} {
		start := make(chan struct{})
		if alreadyStarted {
			close(start)
		}
		canceled := make(chan struct{})
		close(canceled)
		p := NewPlayback(start, make(chan PlaybackResult))
		started, terminal := p.WaitStart(canceled)
		if started || terminal != nil {
			t.Fatalf("canceled lifecycle admitted: started=%v terminal=%+v", started, terminal)
		}
	}
}
