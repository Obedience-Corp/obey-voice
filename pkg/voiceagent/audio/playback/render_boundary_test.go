package playback

import (
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestRenderBoundaryFollowsDeviceWrite(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "revoked"}[revoked], func(t *testing.T) {
			b := &audio.FirstRenderBoundary{}
			fence := &testPlaybackFence{done: make(chan struct{})}
			segment := newPlaybackSegment()
			segment.attempt = audio.PlaybackAttempt{ID: "original", CaptureEpoch: 7, Fence: fence, Render: b}
			player := NewPlayer()
			player.queue = append(player.queue, segment)
			out := make([]byte, 4)
			player.onData(out, nil, 2) // queued, but synthesis has no PCM yet
			if _, ok := b.FirstRender(); ok {
				t.Fatal("queued empty segment published a device write")
			}
			segment.append([]int16{4000, 4000, 4000, 4000})
			segment.finishInput(nil)
			if revoked {
				fence.revoke()
			}
			before := time.Now()
			player.onData(out, nil, 2)
			after := time.Now()
			at, ok := b.FirstRender()
			if revoked {
				if ok {
					t.Fatal("revoked attempt published a device write")
				}
				return
			}
			if !ok || at.Before(before) || at.After(after) {
				t.Fatalf("missing or late callback observation: %v, %t", at, ok)
			}
			player.onData(out, nil, 2)
			next := newPlaybackSegment()
			next.attempt = segment.attempt
			next.append([]int16{3000, 3000})
			next.finishInput(nil)
			player.queue = append(player.queue, next)
			player.onData(out, nil, 2)
			if got, _ := b.FirstRender(); got != at {
				t.Fatal("later segment changed attempt's first write")
			}
			player.onData(out, nil, 2) // idle callback
			if got, _ := b.FirstRender(); got != at {
				t.Fatal("idle render changed the boundary")
			}
		})
	}
}
