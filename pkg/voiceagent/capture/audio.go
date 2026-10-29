package capture

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// ConversationAudio names the playable mixed microphone/render timeline.
const ConversationAudio = "conversation.wav"
const maxAudioBytes = 1 << 30
const maxAudioSamples = maxAudioBytes / 2

type AudioTrack struct {
	Available     bool   `json:"available"`
	Frames        uint64 `json:"frames"`
	Samples       uint64 `json:"samples"`
	DroppedFrames uint64 `json:"dropped_frames"`
}
type AudioManifest struct {
	File       string     `json:"file,omitempty"`
	Status     string     `json:"status"`
	SampleRate int        `json:"sample_rate,omitempty"`
	User       AudioTrack `json:"user"`
	Agents     AudioTrack `json:"agents"`
	Error      string     `json:"error,omitempty"`
}

type audioFailure struct{ err error }

type audioTimeline struct {
	sequence   int64
	rate       int
	nextOffset int64
	resampler  audio.StreamResampler
}

type audioRecorder struct {
	userTimeline, agentTimeline audioTimeline
	failure                     atomic.Pointer[audioFailure]
	writer                      *audio.WAVWriter
	start                       time.Time
	stop                        time.Time
	inputEnabled                atomic.Bool
	detachUser, detachAgents    func() audio.RecordingStats
	done                        chan struct{}
	health                      AudioManifest // owned by disk worker until done
	err                         error
	stopOnce                    sync.Once
}

func startAudio(path string, start time.Time, sources audio.RecordingSources, inputEnabled bool) (*audioRecorder, error) {
	if sources.User == nil && sources.Agents == nil {
		return nil, nil
	}
	w, err := audio.NewWAVWriter(path, audio.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("create conversation audio: %w", err)
	}
	r := &audioRecorder{writer: w, start: start, done: make(chan struct{}), health: AudioManifest{File: ConversationAudio, Status: "recording", SampleRate: audio.SampleRate}}
	r.inputEnabled.Store(inputEnabled)
	var user, agents <-chan audio.Frame
	if sources.User != nil {
		user, r.detachUser = sources.User.SubscribeRecording(128, r.inputEnabled.Load)
		r.health.User.Available = true
	}
	if sources.Agents != nil {
		agents, r.detachAgents = sources.Agents.SubscribeRecording(128, nil)
		r.health.Agents.Available = true
	}
	go func() {
		defer close(r.done)
		for user != nil || agents != nil {
			select {
			case frame, ok := <-user:
				if !ok {
					user = nil
					continue
				}
				r.write(frame, &r.health.User, &r.userTimeline)
			case frame, ok := <-agents:
				if !ok {
					agents = nil
					continue
				}
				r.write(frame, &r.health.Agents, &r.agentTimeline)
			}
		}
	}()
	return r, nil
}
func (r *audioRecorder) write(frame audio.Frame, track *AudioTrack, timeline *audioTimeline) {
	if r.err != nil {
		return
	}
	if frame.SampleRate <= 0 || frame.Channels != 1 || frame.StartedAt.IsZero() {
		r.fail(errors.New("conversation audio source has invalid format or missing timestamp"))
		return
	}
	track.Frames++
	offset := frame.StartedAt.Sub(r.start).Nanoseconds() * audio.SampleRate / int64(time.Second)
	if frame.Sequence > 0 && frame.Sequence == timeline.sequence+1 && frame.SampleRate == timeline.rate && !frame.CaptureGap.DeviceDiscontinuity {
		// Continuous source callbacks follow their sample clock, not scheduler jitter.
		offset = timeline.nextOffset
	} else {
		timeline.resampler = audio.StreamResampler{}
	}
	timeline.resampler.Configure(frame.SampleRate, audio.SampleRate)
	samples := make([]float32, int(math.Ceil(float64(len(frame.Samples))*audio.SampleRate/float64(frame.SampleRate)))+8)
	samples = samples[:timeline.resampler.Resample(frame.Samples, samples)]
	timeline.sequence, timeline.rate, timeline.nextOffset = frame.Sequence, frame.SampleRate, offset+int64(len(samples))
	if offset < 0 {
		cut := min(int64(len(samples)), -offset)
		samples = samples[cut:]
		offset = 0
	}
	if offset+int64(len(samples)) > maxAudioSamples {
		r.fail(errors.New("conversation audio reached its 1 GiB limit"))
		return
	}
	track.Samples += uint64(len(samples))
	if err := r.writer.MixAt(samples, offset); err != nil {
		r.fail(fmt.Errorf("write conversation audio: %w", err))
	}
}

// detach is short and synchronous. Disk draining/finalization belongs to seal.
func (r *audioRecorder) detach(at time.Time) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		r.stop = at
		if r.detachUser != nil {
			stats := r.detachUser()
			r.health.User.DroppedFrames = stats.DroppedFrames
		}
		if r.detachAgents != nil {
			stats := r.detachAgents()
			r.health.Agents.DroppedFrames = stats.DroppedFrames
		}
	})
}
func (r *audioRecorder) finish() (AudioManifest, error) {
	if r == nil {
		return AudioManifest{Status: "unavailable"}, nil
	}
	<-r.done
	count := max(int64(0), r.stop.Sub(r.start).Nanoseconds()*audio.SampleRate/int64(time.Second))
	if count > maxAudioSamples {
		count = maxAudioSamples
		if r.err == nil {
			r.fail(errors.New("conversation audio reached its 1 GiB limit"))
		}
	}
	if err := r.writer.SetSamples(count); err != nil && r.err == nil {
		r.fail(err)
	}
	if err := r.writer.Sync(); err != nil && r.err == nil {
		r.fail(err)
	}
	if err := r.writer.Close(); err != nil && r.err == nil {
		r.fail(err)
	}
	if r.health.User.DroppedFrames+r.health.Agents.DroppedFrames > 0 && r.err == nil {
		r.fail(errors.New("conversation audio dropped frames; see audio source health"))
	}
	r.health.Status = "complete"
	if r.err != nil {
		r.health.Status = "incomplete"
		r.health.Error = r.err.Error()
	}
	return r.health, r.err
}

func (r *audioRecorder) fail(err error) {
	if r.err == nil {
		r.err = err
		r.failure.Store(&audioFailure{err: err})
	}
}
