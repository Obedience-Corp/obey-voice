package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if n := p.activeCancels(); n != 0 {
		t.Fatalf("active cancels after Done = %d, want 0", n)
	}
}

func TestCompletedPlaybacksReleaseCancels(t *testing.T) {
	p := newRecordingPlayer(nil)
	ctx := context.Background()
	pcm := synthSine(24_000, 480, 440, 0.4)
	for i := 0; i < 20; i++ {
		if err := playPCM(ctx, p, 24_000, pcm); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if n := p.activeCancels(); n != 0 {
			t.Fatalf("after stream %d: active cancels = %d, want 0", i+1, n)
		}
	}

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
	if n := p.activeCancels(); n != 1 {
		t.Fatalf("in-flight cancels = %d, want 1 (only the active drain)", n)
	}
	stream.Close()
	select {
	case <-pb.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not fire")
	}
	if n := p.activeCancels(); n != 0 {
		t.Fatalf("after final Done: active cancels = %d, want 0", n)
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
	case <-pb.Done():
		// Drain may finish normally before Stop or report cancellation/interruption.
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

func TestRecordingSinkDoesNotClaimSpeakerReceipt(t *testing.T) {
	if got := newRecordingPlayer(nil).SinkKind(); got != audio.SinkNone {
		t.Fatalf("recording sink = %s", got)
	}
	if got := newRecordingPlayer(&countingEngine{}).SinkKind(); got != audio.SinkLocalSpeaker {
		t.Fatalf("speaker sink = %s", got)
	}
}

type bufferedStartEngine struct {
	countingEngine
	fail bool
}

func (e *bufferedStartEngine) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if e.fail {
		return nil, errors.New("speaker unavailable")
	}
	select {
	case <-stream.Frames(): // A real player may need input before returning.
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	started, done := make(chan struct{}), make(chan audio.PlaybackResult, 1)
	close(started)
	go func() {
		for range stream.Frames() {
		}
		done <- audio.PlaybackResult{AudioSeconds: 0.02, Underruns: 2}
		close(done)
	}()
	return audio.NewPlayback(started, done), nil
}

func TestSpeakerTeeFeedsBeforeWaitingForStartAndPreservesResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := newRecordingPlayer(&bufferedStartEngine{})
	stream := audio.NewPCMStream(ctx)
	_ = stream.SetSampleRate(24000)
	pb, err := p.PlayStream(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Write(make([]float32, 480))
	stream.Close()
	select {
	case result := <-pb.Done():
		if result.Err != nil || result.AudioSeconds != .02 || result.Underruns != 2 {
			t.Fatalf("speaker result lost: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("tee waited for player before feeding PCM")
	}
}

func TestSpeakerTeeFailureDoesNotBecomeSuccessfulRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := newRecordingPlayer(&bufferedStartEngine{fail: true})
	if err := playPCM(ctx, p, 24000, make([]float32, 480)); err == nil || !strings.Contains(err.Error(), "speaker unavailable") {
		t.Fatalf("speaker failure = %v", err)
	}
}

func TestLateInterruptedPCMDoesNotEnterNextRecording(t *testing.T) {
	p := newRecordingPlayer(nil)
	if err := p.BeginTurn(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	old := audio.NewPCMStream(context.Background())
	_ = old.SetSampleRate(24000)
	pb, err := p.PlayStream(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	<-pb.Started()
	p.Stop()
	if err := p.BeginTurn(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_ = old.Write([]float32{.9, .9, .9}) // Delayed producer ignores cancellation.
	old.Close()
	select {
	case <-pb.Done():
	case <-time.After(time.Second):
		t.Fatal("old PCM did not drain")
	}
	if err := playPCM(context.Background(), p, 24000, []float32{.1, .2}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.samples) != 2 || p.samples[0] != .1 || p.samples[1] != .2 {
		t.Fatalf("old PCM crossed recording boundary: %v", p.samples)
	}
}
