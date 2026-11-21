package netapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// probeEngine is a local speaker that records the stream it was handed instead
// of playing it. The fanout builds that stream itself, so this is the only
// place the properties it carried can be observed.
type probeEngine struct {
	got chan *audio.PCMStream
}

func newProbeEngine() *probeEngine {
	return &probeEngine{got: make(chan *audio.PCMStream, 1)}
}

func (p *probeEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	select {
	case p.got <- stream:
	default:
	}
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		close(started)
		for range stream.Frames() {
		}
		done <- audio.PlaybackResult{}
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}

func (p *probeEngine) Stop()           {}
func (p *probeEngine) IsPlaying() bool { return false }
func (p *probeEngine) Close() error    { return nil }

func (p *probeEngine) received(t *testing.T) *audio.PCMStream {
	t.Helper()
	select {
	case stream := <-p.got:
		return stream
	case <-time.After(5 * time.Second):
		t.Fatal("the local engine was never handed a stream")
		return nil
	}
}

// Under `obey-voice serve` the fanout IS the pipeline's Player, so the local
// speaker never sees the stream the synthesizer produced — it sees one the
// fanout built. Everything that says how the audio must be played has to
// survive that tee, or the feature is inert on exactly the path the Mac app
// uses while doctor reports it as on.
func TestFanoutTeeCarriesStreamProperties(t *testing.T) {
	probe := newProbeEngine()
	fanout := NewAudioFanout(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := audio.NewPCMStream(ctx)
	source.SetIncremental(true)
	source.SetStallBudget(7 * time.Second)
	if err := source.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer source.Close()
		_ = source.Write(make([]float32, 480))
	}()

	playback, err := fanout.PlayStream(ctx, source)
	if err != nil {
		t.Fatalf("PlayStream: %v", err)
	}

	teed := probe.received(t)
	if !teed.Incremental() {
		t.Error("the teed stream lost Incremental: Stage B chunks would be buffered whole under serve")
	}
	if got := teed.StallBudget(); got != 7*time.Second {
		t.Errorf("teed StallBudget = %v, want 7s: a stalled engine would never reach the fallback voice under serve", got)
	}
	if got, err := teed.WaitReady(ctx); err != nil || got != 24000 {
		t.Errorf("teed sample rate = %d (%v), want 24000", got, err)
	}
	<-playback.Done()
}

// A whole-utterance stream must tee as whole-utterance. Derive copies whatever
// is there, so the default has to be the safe one.
func TestFanoutTeeDoesNotInventStreaming(t *testing.T) {
	probe := newProbeEngine()
	fanout := NewAudioFanout(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source := audio.NewPCMStream(ctx)
	if err := source.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer source.Close()
		_ = source.Write(make([]float32, 480))
	}()

	playback, err := fanout.PlayStream(ctx, source)
	if err != nil {
		t.Fatalf("PlayStream: %v", err)
	}
	teed := probe.received(t)
	if teed.Incremental() {
		t.Error("the tee marked a whole-utterance stream incremental")
	}
	if teed.StallBudget() != 0 {
		t.Errorf("teed StallBudget = %v, want 0 for an unbounded stream", teed.StallBudget())
	}
	<-playback.Done()
}

// The startup deadline includes the wait for the sample rate. A provider may
// announce the rate partway through startup without sending any PCM.
func TestFanoutStreamClientStartupStall(t *testing.T) {
	for _, announce := range []bool{false, true} {
		name := "without sample rate"
		if announce {
			name = "sample rate arrives before PCM"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				h := newHub()
				conn := newTestConn(4, 4)
				h.add(conn)
				h.setConnAudio(conn, true, false)
				fanout := NewAudioFanout(nil)
				fanout.AttachHub(h)
				src := audio.NewPCMStream(ctx)
				defer src.Close()
				src.SetStallBudgets(time.Second, 20*time.Millisecond)
				if announce {
					time.AfterFunc(800*time.Millisecond, func() {
						if err := src.SetSampleRate(24000); err != nil {
							t.Error(err)
						}
					})
				}
				start := time.Now()
				pb, err := fanout.PlayStream(ctx, src)
				if pb != nil || !errors.Is(err, audio.ErrStreamStalled) || time.Since(start) != time.Second {
					t.Fatalf("PlayStream = %v, %v after %v", pb, err, time.Since(start))
				}
				if len(conn.audio) != 0 {
					t.Fatal("stream client got audio for a segment that never started")
				}
			})
		})
	}
}

func TestFanoutStreamClientFrameGap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local audio.Engine
	}{
		{"remote only", nil},
		{"local and remote", newProbeEngine()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				h := newHub()
				conn := newTestConn(4, 4)
				h.add(conn)
				h.setConnAudio(conn, true, false)
				fanout := NewAudioFanout(tc.local)
				fanout.AttachHub(h)
				src := audio.NewPCMStream(ctx)
				defer src.Close()
				const gap = 20 * time.Millisecond
				src.SetStallBudgets(time.Second, gap)
				if err := src.SetSampleRate(24000); err != nil {
					t.Fatal(err)
				}
				// A healthy first batch can take longer than the frame gap.
				time.AfterFunc(100*time.Millisecond, func() {
					if err := src.Write([]float32{0.5}); err != nil {
						t.Error(err)
					}
				})
				pb, err := fanout.PlayStream(ctx, src)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				time.Sleep(gap / 2)
				if err := src.Write([]float32{0.25}); err != nil {
					t.Fatal(err)
				}
				result := <-pb.Done()
				if !errors.Is(result.Err, audio.ErrStreamStalled) || result.Interrupted || time.Since(start) != gap+gap/2 {
					t.Fatalf("terminal result %+v after %v", result, time.Since(start))
				}
				// The wire segment ends once, and later PCM is drained so a
				// recovering producer can finish without writing stale audio.
				for range 16 {
					if err := src.Write([]float32{1}); err != nil {
						t.Fatal(err)
					}
				}
				if len(conn.audio) != 3 {
					t.Fatalf("stream client got %d messages, want two chunks and an end", len(conn.audio))
				}
				for _, want := range []string{"audio_chunk", "audio_chunk", "audio_end"} {
					var msg struct{ Type, Reason string }
					if err := json.Unmarshal(<-conn.audio, &msg); err != nil || msg.Type != want || (want == "audio_end" && msg.Reason != "error") {
						t.Fatalf("wire message = %+v, %v; want %s", msg, err, want)
					}
				}
			})
		})
	}
}
