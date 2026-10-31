package personaruntime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type liveNativeInput struct {
	event  utterance.Event
	starts atomic.Int32
}

func (p *liveNativeInput) Available() bool { return true }
func (p *liveNativeInput) Start(context.Context) (utterance.Session, error) {
	p.starts.Add(1)
	ch := make(chan utterance.Event, 1)
	ch <- p.event
	close(ch)
	return &liveNativeSession{events: ch}, nil
}

type liveNativeSession struct{ events chan utterance.Event }

func (s *liveNativeSession) Events() <-chan utterance.Event { return s.events }
func (*liveNativeSession) Close() error                     { return nil }

type liveNativeMonitor struct{}

func (*liveNativeMonitor) Subscribe(int) (int, <-chan []float32) { return 1, make(chan []float32) }
func (*liveNativeMonitor) Unsubscribe(int)                       {}
func (*liveNativeMonitor) Reset()                                {}

type liveSoftwareSink struct {
	mu       sync.Mutex
	segments []map[string]any
}

func (*liveSoftwareSink) SinkKind() string { return audio.SinkNone }
func (*liveSoftwareSink) Stop()            {}
func (*liveSoftwareSink) IsPlaying() bool  { return false }
func (*liveSoftwareSink) Close() error     { return nil }
func (s *liveSoftwareSink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		rate, err := stream.WaitReady(ctx)
		hash := sha256.New()
		count := 0
		announced := false
		for frame := range stream.Frames() {
			if len(frame) > 0 && !announced {
				close(started)
				announced = true
			}
			data := make([]byte, len(frame)*4)
			for i, sample := range frame {
				binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(sample))
			}
			_, _ = hash.Write(data)
			count += len(frame)
		}
		if !announced {
			close(started)
		}
		if err == nil {
			err = stream.Err()
		}
		s.mu.Lock()
		s.segments = append(s.segments, map[string]any{"samples": count, "rate": rate, "float32_sha256": hex.EncodeToString(hash.Sum(nil)), "sink": "software_drain"})
		s.mu.Unlock()
		duration := 0.
		if rate > 0 {
			duration = float64(count) / float64(rate)
		}
		done <- audio.PlaybackResult{Err: err, AudioSeconds: duration, SynthesisEndedAt: stream.ClosedAt()}
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}
func (s *liveSoftwareSink) snapshot() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.segments...)
}

type liveRequestObserver struct {
	base        http.RoundTripper
	mu          sync.Mutex
	requests    []map[string]any
	active, max atomic.Int32
}
type liveObservedBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *liveObservedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.finish)
	return err
}
func (r *liveRequestObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	inference := req.URL.Path == "/api/chat" || req.URL.Path == "/v1/audio/speech" || req.URL.Path == "/v1/audio/transcriptions"
	if inference {
		entry := map[string]any{"route": req.URL.Path}
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err == nil {
				data, readErr := io.ReadAll(io.LimitReader(body, 4<<20))
				_ = body.Close()
				if readErr == nil {
					var payload struct {
						Model    string `json:"model"`
						Stream   bool   `json:"stream"`
						Input    string `json:"input"`
						Messages []struct {
							Role, Content string
							Images        [][]byte
						} `json:"messages"`
					}
					if err := json.Unmarshal(data, &payload); err == nil {
						entry["model"], entry["stream"], entry["speech_input"] = payload.Model, payload.Stream, payload.Input
						messages := []map[string]any{}
						for _, message := range payload.Messages {
							contentSum := sha256.Sum256([]byte(message.Content))
							metadata := map[string]any{"role": message.Role, "content_sha256": hex.EncodeToString(contentSum[:]), "content_bytes": len(message.Content)}
							if message.Role == "user" {
								metadata["user_text"] = message.Content
							}
							images := []map[string]any{}
							for _, data := range message.Images {
								sum := sha256.Sum256(data)
								images = append(images, map[string]any{"sha256": hex.EncodeToString(sum[:]), "bytes": len(data), "riff_wave": len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"})
							}
							metadata["audio_wire"] = images
							messages = append(messages, metadata)
						}
						entry["messages"] = messages
					}
				}
			}
		}
		r.mu.Lock()
		r.requests = append(r.requests, entry)
		r.mu.Unlock()
		active := r.active.Add(1)
		for {
			old := r.max.Load()
			if active <= old || r.max.CompareAndSwap(old, active) {
				break
			}
		}
	}
	response, err := r.base.RoundTrip(req)
	if inference {
		if err != nil {
			r.active.Add(-1)
		} else {
			response.Body = &liveObservedBody{ReadCloser: response.Body, finish: func() { r.active.Add(-1) }}
		}
	}
	return response, err
}
func (r *liveRequestObserver) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.requests...)
}
func nativeFixtureSpan(samples int64) []utterance.Span {
	return []utterance.Span{{SampleOffset: 0, SampleCount: int(samples), DeviceGeneration: 1}}
}
func nativeFixtureTimes(samples int64) (time.Time, time.Time) {
	end := time.Now()
	return end.Add(-time.Duration(samples) * time.Second / utterance.SampleRate), end
}

func (r *liveRequestObserver) CloseIdleConnections() {
	if closer, ok := r.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
