package capture

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// A 10 kHz component is valid in the provider's 24 kHz PCM. Recording must
// not fold it into a lower audible frequency by decimating to the STT rate.
func TestRecordingPreservesPlaybackBandwidth(t *testing.T) {
	const sourceRate, frequency, blocks, blockSize = 24000, 10000, 100, 512
	path := filepath.Join(t.TempDir(), ConversationAudio)
	start := time.Now()
	var feed audio.RecordingFeed
	r, err := startAudio(path, start, audio.RecordingSources{Agents: &feed}, false)
	if err != nil {
		t.Fatal(err)
	}
	for block := range blocks {
		pcm := make([]byte, blockSize*2)
		for i := range blockSize {
			sample := int16(10000 * math.Sin(2*math.Pi*frequency*float64(block*blockSize+i)/sourceRate))
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		feed.PublishS16(pcm, sourceRate, start.Add(time.Duration(block*blockSize)*time.Second/sourceRate))
	}
	r.detach(start.Add(time.Duration(blocks*blockSize) * time.Second / sourceRate))
	if _, err := r.finish(); err != nil {
		t.Fatal(err)
	}
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatal(err)
	}
	// Measure the saved signal itself, ignoring the short interpolation tail.
	end := len(samples) - rate/100
	crossings := 0
	for i := 1; i < end; i++ {
		if samples[i-1] <= 0 && samples[i] > 0 {
			crossings++
		}
	}
	measured := float64(crossings) * float64(rate) / float64(end)
	if math.Abs(measured-frequency) > 10 {
		t.Fatalf("saved 10 kHz playback as %.1f Hz at %d Hz sample rate", measured, rate)
	}
}

func recordingFrame(at time.Time, rate int, duration time.Duration, value float32) audio.Frame {
	samples := make([]float32, int(duration)*rate/int(time.Second))
	for i := range samples {
		samples[i] = value
	}
	return audio.Frame{Samples: samples, SampleRate: rate, Channels: 1, StartedAt: at, Duration: duration}
}
func TestConversationWAVContainsBothSidesOnSharedTimeline(t *testing.T) {
	start := time.Now()
	var mic, speaker audio.RecordingFeed
	path := filepath.Join(t.TempDir(), ConversationAudio)
	r, err := startAudio(path, start, audio.RecordingSources{User: &mic, Agents: &speaker}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Include a callback straddling start, overlapping speech at another rate,
	// silence, a paused-mic callback, and a callback extending beyond stop.
	mic.Publish(recordingFrame(start.Add(-100*time.Millisecond), 16000, 200*time.Millisecond, .1))
	mic.Publish(recordingFrame(start.Add(200*time.Millisecond), 16000, 200*time.Millisecond, .2))
	speaker.Publish(recordingFrame(start.Add(300*time.Millisecond), 48000, 200*time.Millisecond, .3))
	r.inputEnabled.Store(false)
	mic.Publish(recordingFrame(start.Add(600*time.Millisecond), 16000, 100*time.Millisecond, .8))
	speaker.Publish(recordingFrame(start.Add(900*time.Millisecond), 48000, 300*time.Millisecond, .4))
	r.detach(start.Add(time.Second))
	mic.Publish(recordingFrame(start, 16000, time.Second, .9)) // late callback is detached
	health, err := r.finish()
	if err != nil {
		t.Fatal(err)
	}
	got, rate, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatal(err)
	}
	if rate != RecordingSampleRate || len(got) != RecordingSampleRate {
		t.Fatalf("format %d Hz, %d samples", rate, len(got))
	}
	for _, tc := range []struct {
		ms   int
		want float32
	}{{50, .1}, {150, 0}, {250, .2}, {350, .5}, {450, .3}, {650, 0}, {950, .4}} {
		if value := got[tc.ms*RecordingSampleRate/1000]; math.Abs(float64(value-tc.want)) > .001 {
			t.Errorf("at %dms: %v, want %v", tc.ms, value, tc.want)
		}
	}
	if health.Status != "complete" || health.User.Frames != 2 || health.Agents.Frames != 2 {
		t.Fatalf("health=%+v", health)
	}
	if stat, err := os.Stat(path); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("private audio permissions: %v %v", stat, err)
	}
}

func TestSessionSealsPlayableAudioAndReportsUnavailableSources(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "diagnostics only", true: "audio"}[available], func(t *testing.T) {
			var mic, speaker audio.RecordingFeed
			sources := audio.RecordingSources{}
			if available {
				sources = audio.RecordingSources{User: &mic, Agents: &speaker}
			}
			s, err := Start(Options{Root: t.TempDir(), Mode: ModeTalk, Audio: sources, InputEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if available {
				mic.Publish(recordingFrame(s.manifest.StartedAt, 16000, time.Second, .1))
				speaker.Publish(recordingFrame(s.manifest.StartedAt, 48000, time.Second, .2))
				time.Sleep(20 * time.Millisecond)
			}
			if err := s.Stop(nil); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(s.Dir(), "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if available {
				if manifest.Audio.Status != "complete" || !manifest.Audio.User.Available || !manifest.Audio.Agents.Available {
					t.Fatalf("manifest=%+v", manifest.Audio)
				}
				pcm, _, err := audio.ReadWAVFloat32(filepath.Join(s.Dir(), manifest.Audio.File))
				if err != nil || len(pcm) == 0 || slices.Max(pcm) < .29 {
					t.Fatalf("missing playable mixed audio: %v %v", pcm, err)
				}
			} else if manifest.Audio.Status != "unavailable" || manifest.Audio.File != "" {
				t.Fatalf("invented audio: %+v", manifest.Audio)
			}
		})
	}
}

func TestAudioWriteFailurePreventsCompleteSeal(t *testing.T) {
	var mic audio.RecordingFeed
	s, err := Start(Options{Root: t.TempDir(), Mode: ModeTalk, Audio: audio.RecordingSources{User: &mic}, InputEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.audio.writer.Close(); err != nil {
		t.Fatal(err)
	}
	mic.Publish(recordingFrame(s.manifest.StartedAt, 16000, time.Second, .1))
	if err := s.Stop(nil); err == nil {
		t.Fatal("failed audio was sealed successfully")
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Status != "incomplete" || m.Audio.Status != "incomplete" || m.Audio.Error == "" {
		t.Fatalf("health=%+v", m)
	}
}

type overrunAudioSource struct {
	feed  audio.RecordingFeed
	frame audio.Frame
}

func (s *overrunAudioSource) SubscribeRecording(_ int, enabled func() bool) (<-chan audio.Frame, func() audio.RecordingStats) {
	frames, stop := s.feed.SubscribeRecording(1, enabled)
	s.feed.Publish(s.frame)
	s.feed.Publish(s.frame)
	return frames, stop
}
func TestAudioQueueLossCannotSealAsComplete(t *testing.T) {
	start := time.Now()
	source := &overrunAudioSource{frame: recordingFrame(start, 16000, time.Millisecond, .1)}
	r, err := startAudio(filepath.Join(t.TempDir(), ConversationAudio), start, audio.RecordingSources{User: source}, true)
	if err != nil {
		t.Fatal(err)
	}
	r.detach(start.Add(time.Millisecond))
	health, err := r.finish()
	if err == nil || health.Status != "incomplete" || health.User.DroppedFrames != 1 {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestRecordedTonePreservesPhaseAcrossJitteryCallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConversationAudio)
	w, err := audio.NewWAVWriter(path, RecordingSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := &audioRecorder{writer: w, start: start, done: make(chan struct{})}
	var all []float32
	const rate = 44100
	for block := 0; block < 100; block++ {
		samples := make([]float32, 512)
		for i := range samples {
			samples[i] = float32(.2 * math.Sin(2*math.Pi*440*float64(block*512+i)/rate))
		}
		all = append(all, samples...)
		at := start.Add(time.Duration(block*512) * time.Second / rate)
		if block > 0 {
			at = at.Add(time.Duration(block%3-1) * 2 * time.Millisecond)
		}
		r.write(audio.Frame{Samples: samples, SampleRate: rate, Channels: 1, StartedAt: at, Sequence: int64(block + 1)}, &r.health.Agents, &r.agentTimeline)
	}
	r.stop = start.Add(time.Duration(len(all)) * time.Second / rate)
	close(r.done)
	if _, err := r.finish(); err != nil {
		t.Fatal(err)
	}
	got, _, err := audio.ReadWAVFloat32(path)
	if err != nil {
		t.Fatal(err)
	}
	want := audio.ResampleLinear(all, rate, RecordingSampleRate)
	for i := range min(len(got), len(want)) - 2 {
		if math.Abs(float64(got[i]-want[i])) > .001 {
			t.Fatalf("phase jump at sample %d: %v versus %v", i, got[i], want[i])
		}
	}
}
