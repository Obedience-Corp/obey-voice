package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

func TestPlayStreamNeverReturnsNilPlayback(t *testing.T) {
	t.Parallel()
	p := newRecordingPlayer(nil)
	pb, err := p.PlayStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil with a Playback handle", err)
	}
	if pb == nil {
		t.Fatal("PlayStream returned nil Playback")
	}
	res := <-pb.Done()
	if res.Err == nil {
		t.Fatal("want error on nil stream")
	}
}

func TestPlayStreamDrainsWhenWaitReadyFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stream := audio.NewPCMStream(ctx)
	stream.Close() // no sample rate
	p := newRecordingPlayer(nil)
	pb, err := p.PlayStream(ctx, stream)
	if err != nil || pb == nil {
		t.Fatalf("PlayStream = (%v, %v), want non-nil Playback", pb, err)
	}
	select {
	case <-pb.Done():
	case <-time.After(time.Second):
		t.Fatal("WaitReady failure did not complete playback")
	}
}

func TestIsPlayingOnlyDuringDrain(t *testing.T) {
	ctx := context.Background()
	p := newRecordingPlayer(nil)
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24_000); err != nil {
		t.Fatal(err)
	}
	pb, err := p.PlayStream(ctx, stream)
	if err != nil || pb == nil {
		t.Fatalf("PlayStream = (%v, %v)", pb, err)
	}
	select {
	case <-pb.Started():
	case <-time.After(time.Second):
		t.Fatal("Started() did not fire")
	}
	if !p.IsPlaying() {
		t.Fatal("IsPlaying false while drain is in flight")
	}
	if err := stream.Write(synthSine(24_000, 480, 440, 0.4)); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	select {
	case <-pb.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not fire")
	}
	if p.IsPlaying() {
		t.Fatal("IsPlaying true after Done")
	}
}

func TestTwoPlayStreamsConcatenate(t *testing.T) {
	dir := t.TempDir()
	p := newRecordingPlayer(nil)
	if err := p.BeginTurn(dir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := synthSine(24_000, 1_200, 440, 0.4)
	b := synthSine(24_000, 800, 440, 0.4)
	if err := playPCM(ctx, p, 24_000, a); err != nil {
		t.Fatal(err)
	}
	if err := playPCM(ctx, p, 24_000, b); err != nil {
		t.Fatal(err)
	}
	if err := p.EndTurn(); err != nil {
		t.Fatal(err)
	}
	got, rate, err := audio.ReadWAVFloat32(filepath.Join(dir, "playback.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24_000 {
		t.Fatalf("rate = %d, want 24000", rate)
	}
	if len(got) != len(a)+len(b) {
		t.Fatalf("samples = %d, want %d", len(got), len(a)+len(b))
	}
}

func TestEndTurnSkipsEmptyWav(t *testing.T) {
	dir := t.TempDir()
	p := newRecordingPlayer(nil)
	if err := p.BeginTurn(dir); err != nil {
		t.Fatal(err)
	}
	if err := p.EndTurn(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "playback.wav")); !os.IsNotExist(err) {
		t.Fatalf("empty turn must not write an unreadable wav, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "analysis.json")); err != nil {
		t.Fatalf("analysis.json: %v", err)
	}
}

func TestSpeakersTeeSameSampleCount(t *testing.T) {
	fake := &countingEngine{}
	p := newRecordingPlayer(fake)
	dir := t.TempDir()
	if err := p.BeginTurn(dir); err != nil {
		t.Fatal(err)
	}
	samples := synthSine(24_000, 2_400, 440, 0.4)
	if err := playPCM(context.Background(), p, 24_000, samples); err != nil {
		t.Fatal(err)
	}
	if got := fake.count(); got != len(samples) {
		t.Fatalf("tee samples = %d, want %d", got, len(samples))
	}
}

func TestStopClearsIsPlaying(t *testing.T) {
	ctx := context.Background()
	p := newRecordingPlayer(nil)
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(24_000); err != nil {
		t.Fatal(err)
	}
	pb, err := p.PlayStream(ctx, stream)
	if err != nil || pb == nil {
		t.Fatalf("PlayStream = (%v, %v)", pb, err)
	}
	<-pb.Started()
	p.Stop()
	if p.IsPlaying() {
		t.Fatal("IsPlaying true after Stop")
	}
	stream.Close()
	select {
	case res := <-pb.Done():
		if !res.Interrupted && res.Err == nil {
			// drain may finish as cancelled or interrupted; either is fine
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish the in-flight drain")
	}
}

type countingEngine struct {
	mu      sync.Mutex
	samples int
}

func (c *countingEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	go func() {
		defer close(done)
		if _, err := stream.WaitReady(ctx); err != nil {
			drainDiscard(stream)
			close(started)
			done <- audio.PlaybackResult{Err: err}
			return
		}
		close(started)
		n := 0
		for frame := range stream.Frames() {
			n += len(frame)
		}
		c.mu.Lock()
		c.samples = n
		c.mu.Unlock()
		done <- audio.PlaybackResult{}
	}()
	return audio.NewPlayback(started, done), nil
}

func (c *countingEngine) Stop()           {}
func (c *countingEngine) IsPlaying() bool { return false }
func (c *countingEngine) Close() error    { return nil }

func (c *countingEngine) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.samples
}

func playPCM(ctx context.Context, p *recordingPlayer, rate int, samples []float32) error {
	if p == nil {
		return errors.New("nil recording player")
	}
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		return err
	}
	pb, err := p.PlayStream(ctx, stream)
	if err != nil {
		return err
	}
	if pb == nil {
		return errors.New("nil playback")
	}
	if err := stream.Write(samples); err != nil {
		stream.Close()
		return err
	}
	stream.Close()
	res := <-pb.Done()
	if res.Err != nil {
		return res.Err
	}
	return nil
}
