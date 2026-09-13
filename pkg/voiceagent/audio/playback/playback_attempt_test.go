package playback

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"sync"
	"testing"
)

type testPlaybackFence struct {
	mu      sync.Mutex
	revoked bool
	done    chan struct{}
}

func (f *testPlaybackFence) Write(fn func()) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked {
		return false
	}
	fn()
	return true
}
func (f *testPlaybackFence) Valid() bool           { f.mu.Lock(); defer f.mu.Unlock(); return !f.revoked }
func (f *testPlaybackFence) Done() <-chan struct{} { return f.done }
func (f *testPlaybackFence) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.revoked {
		f.revoked = true
		close(f.done)
	}
}

// Exercise the real device callback with a prepared segment, without opening
// a microphone or speaker. Revocation after enqueue must silence its first
// write; revocation after one write must silence every subsequent callback.
func TestPlaybackAttemptFencesDeviceCallback(t *testing.T) {
	for _, beforeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_first", false: "during_playback"}[beforeFirst], func(t *testing.T) {
			fence := &testPlaybackFence{done: make(chan struct{})}
			segment := newPlaybackSegment()
			segment.attempt = audio.PlaybackAttempt{ID: "attempt-original", CaptureEpoch: 7, Fence: fence}
			segment.append([]int16{4000, 4000, 4000, 4000})
			segment.finishInput(nil)
			player := NewPlayer()
			player.queue = append(player.queue, segment)
			if !beforeFirst {
				out := make([]byte, 4)
				player.onData(out, nil, 2)
				if out[0] == 0 && out[1] == 0 {
					t.Fatal("valid attempt produced no PCM")
				}
			}
			fence.revoke()
			out := []byte{255, 255, 255, 255}
			player.onData(out, nil, 2)
			for _, b := range out {
				if b != 0 {
					t.Fatalf("stale attempt wrote PCM: %v", out)
				}
			}
			result := <-segment.doneCh
			if !result.Interrupted || result.AttemptID != "attempt-original" || result.CaptureEpoch != 7 {
				t.Fatalf("incorrect attempt receipt: %+v", result)
			}
		})
	}
}
