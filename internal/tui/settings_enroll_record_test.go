package tui

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// pushChunkScale encodes a chunk's publish index in its sample values: chunk k
// carries k/pushChunkScale, which survives the WAV's int16 quantization and
// reads back with a round.
const pushChunkScale = 1000

// pushEnrollCapture publishes on a clock the way the device does — a fresh
// chunk every interval whether or not anyone is reading, dropped into a full
// subscriber queue rather than blocking. That is what makes the inter-clip gap
// observable: audio spoken during the pause is published to nobody, so a clip
// that starts with it is reading a backlog instead of the microphone.
type pushEnrollCapture struct {
	mu   sync.Mutex
	next int
	subs map[int]chan []float32

	stop chan struct{}
	done chan struct{}
}

func newPushEnrollCapture(t *testing.T, interval time.Duration) *pushEnrollCapture {
	t.Helper()
	c := &pushEnrollCapture{
		subs: map[int]chan []float32{},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go c.publish(interval)
	t.Cleanup(func() {
		close(c.stop)
		<-c.done
	})
	return c
}

func (c *pushEnrollCapture) publish(interval time.Duration) {
	defer close(c.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for index := 1; ; index++ {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		}
		chunk := make([]float32, audio.ChunkSize)
		for i := range chunk {
			chunk[i] = float32(index) / pushChunkScale
		}
		c.mu.Lock()
		for _, ch := range c.subs {
			select {
			case ch <- chunk:
			default:
			}
		}
		c.mu.Unlock()
	}
}

func (c *pushEnrollCapture) Subscribe(buffer int) (int, <-chan []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	ch := make(chan []float32, max(buffer, 1))
	c.subs[c.next] = ch
	return c.next, ch
}

// Unsubscribe drops the queue without closing it: a closed channel is how a
// stopped microphone looks to recordEnrollClip, and this one is still running.
func (c *pushEnrollCapture) Unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, id)
}

// shrinkEnrollClip makes a clip short enough that a real inter-clip gap costs
// milliseconds. Tests run sequentially in this package, so the var is safe.
func shrinkEnrollClip(t *testing.T, d time.Duration) {
	t.Helper()
	prev := enrollClipLength
	enrollClipLength = d
	t.Cleanup(func() { enrollClipLength = prev })
}

// clipChunkIndexes reads a recorded WAV back as the sequence of publish indexes
// that reached it, one per chunk.
func clipChunkIndexes(t *testing.T, path string) []int {
	t.Helper()
	samples, _, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(samples)%audio.ChunkSize != 0 {
		t.Fatalf("%s holds %d samples, want whole %d-sample chunks", path, len(samples), audio.ChunkSize)
	}
	indexes := make([]int, 0, len(samples)/audio.ChunkSize)
	for off := 0; off < len(samples); off += audio.ChunkSize {
		indexes = append(indexes, int(math.Round(float64(samples[off])*pushChunkScale)))
	}
	return indexes
}

// TestEnrollClipsStartWhenTheOverlaySaysSo is the anti-backlog test: capture
// runs through the pause between clips, so a subscription held across it would
// let clip 2 fill from queued gap audio and cut the tail off what the user said.
// Each clip must begin after the gap, not where the previous one stopped.
func TestEnrollClipsStartWhenTheOverlaySaysSo(t *testing.T) {
	const (
		pushInterval = 20 * time.Millisecond
		clipGap      = 150 * time.Millisecond
	)
	shrinkEnrollClip(t, 3*audio.SamplesDuration(audio.ChunkSize)) // three chunks

	capture := newPushEnrollCapture(t, pushInterval)
	captureFor := func() (speaker.CaptureSource, func(), error) { return capture, func() {}, nil }

	paths, cleanup, err := recordEnrollClips(t.Context(), captureFor, newEventBridge(64), clipGap)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("recordEnrollClips: %v", err)
	}

	var lastOfPrevious int
	for clip, path := range paths {
		indexes := clipChunkIndexes(t, path)
		if len(indexes) != 3 {
			t.Fatalf("clip %d holds %d chunks, want 3", clip+1, len(indexes))
		}
		// Within a clip the recording is continuous — no dropped frames.
		for i := 1; i < len(indexes); i++ {
			if indexes[i] != indexes[i-1]+1 {
				t.Errorf("clip %d chunks %v are not contiguous", clip+1, indexes)
				break
			}
		}
		// Across the gap it is not: everything published while the overlay was
		// paused went to nobody, so the next clip starts past it.
		if clip > 0 && indexes[0] <= lastOfPrevious+1 {
			t.Errorf("clip %d starts at chunk %d, want past %d — it drained a backlog from the %v gap",
				clip+1, indexes[0], lastOfPrevious+1, clipGap)
		}
		lastOfPrevious = indexes[len(indexes)-1]
	}
}
