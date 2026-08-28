package netapi

import (
	"context"
	"testing"
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
