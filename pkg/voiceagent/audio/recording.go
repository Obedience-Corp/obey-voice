package audio

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"
)

// RecordingSource provides an opt-in, bounded tap of audio actually captured or
// rendered. enabled must be nonblocking (normally an atomic mute flag). Stop
// detaches immediately, closes the stream and returns all subscription loss.
type RecordingSource interface {
	SubscribeRecording(buffer int, enabled func() bool) (<-chan Frame, func() RecordingStats)
}

type RecordingStats struct {
	DroppedFrames uint64 `json:"dropped_frames"`
}
type RecordingSources struct{ User, Agents RecordingSource }

type recordingSubscription struct {
	frames  chan Frame
	enabled func() bool
	dropped uint64
}

// RecordingFeed never performs disk I/O or waits for a consumer on a callback.
// Frames published here own immutable sample storage, shared by subscribers.
type RecordingFeed struct {
	sequence int64
	lastAt   time.Time
	mu       sync.Mutex
	active   atomic.Int32
	subs     map[*recordingSubscription]struct{}
}

func (f *RecordingFeed) SubscribeRecording(buffer int, enabled func() bool) (<-chan Frame, func() RecordingStats) {
	if buffer < 1 {
		buffer = 1
	}
	sub := &recordingSubscription{frames: make(chan Frame, buffer), enabled: enabled}
	f.mu.Lock()
	if f.subs == nil {
		f.subs = make(map[*recordingSubscription]struct{})
	}
	f.subs[sub] = struct{}{}
	f.active.Add(1)
	f.mu.Unlock()
	return sub.frames, func() RecordingStats {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[sub]; ok {
			delete(f.subs, sub)
			f.active.Add(-1)
			close(sub.frames)
		}
		return RecordingStats{DroppedFrames: sub.dropped}
	}
}

func (f *RecordingFeed) Publish(frame Frame) {
	if f.active.Load() == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishLocked(frame)
}
func (f *RecordingFeed) publishLocked(frame Frame) {
	for sub := range f.subs {
		if sub.enabled != nil && !sub.enabled() {
			continue
		}
		select {
		case sub.frames <- frame:
		default:
			sub.dropped++
		}
	}
}

// PublishS16 copies the post-volume mono callback buffer only while a recording
// is attached. The callback's scratch storage is never retained by the writer.
func (f *RecordingFeed) PublishS16(pcm []byte, rate int, at time.Time) {
	if f.active.Load() == 0 || len(pcm) == 0 || rate <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	discontinuity := !f.lastAt.IsZero() && at.Sub(f.lastAt) > 2*time.Duration(len(pcm)/2)*time.Second/time.Duration(rate)
	f.lastAt = at
	room := false
	for sub := range f.subs {
		if (sub.enabled == nil || sub.enabled()) && len(sub.frames) < cap(sub.frames) {
			room = true
			break
		}
	}
	if !room {
		for sub := range f.subs {
			if sub.enabled == nil || sub.enabled() {
				sub.dropped++
			}
		}
		return
	}
	samples := make([]float32, len(pcm)/2)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768
	}
	f.publishLocked(Frame{Samples: samples, SampleRate: rate, Channels: 1, StartedAt: at, Duration: time.Duration(len(samples)) * time.Second / time.Duration(rate), SourceKind: SourceLive, Sequence: f.sequence, CaptureGap: CaptureGap{DeviceDiscontinuity: discontinuity}})
}
