package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// A preview is a voice sample: pauses in generation must not become pauses in
// the sound being demonstrated. Hold synthesis open after its first batch to
// model a server that cannot produce speech as fast as the device consumes it.
func TestVoicePreviewWaitsForCompleteSynthesis(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	if err := stream.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	first := make([]float32, 4800) // 200ms exceeds the player's 150ms pre-roll.
	for i := range first {
		first[i] = .1
	}
	if err := stream.Write(first); err != nil {
		t.Fatal(err)
	}
	player := &previewPCMPlayer{fakePreviewPlayer: newFakePreviewPlayer(), called: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- runPreviewWithStream(ctx, player, stream, nil) }()
	select {
	case <-player.called:
		stream.Close()
		<-done
		t.Fatal("preview reached playback before synthesis finished; a slow producer can leave audible gaps")
	case <-time.After(50 * time.Millisecond):
	}
	// More than the stream's channel capacity: collecting must consume frames,
	// not wait for closure while blocking the producer on a full channel.
	want := append([]float32(nil), first...)
	for i := range 12 {
		chunk := []float32{float32(i) / 20}
		want = append(want, chunk...)
		if err := stream.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	stream.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if player.rate != 24000 || !reflect.DeepEqual(player.samples, want) {
		t.Fatalf("preview changed PCM: rate=%d samples=%v, want 24000 / %v", player.rate, player.samples, want)
	}
}

func TestVoicePreviewDoesNotPlayFailedPartialSynthesis(t *testing.T) {
	stream := audio.NewPCMStream(t.Context())
	_ = stream.SetSampleRate(24000)
	_ = stream.Write([]float32{.1})
	stream.CloseWithError(errors.New("speech server disconnected"))
	player := &previewPCMPlayer{fakePreviewPlayer: newFakePreviewPlayer(), called: make(chan struct{})}
	cleaned := false
	err := runPreviewWithStream(t.Context(), player, stream, func() { cleaned = true })
	if err == nil || !strings.Contains(err.Error(), "speech server disconnected") {
		t.Fatalf("lost synthesis failure: %v", err)
	}
	if !cleaned {
		t.Fatal("failed preview did not release the provider")
	}
	select {
	case <-player.called:
		t.Fatal("played an incomplete voice sample")
	default:
	}
}

func TestVoicePreviewCancellationWhileCollecting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	stream := audio.NewPCMStream(ctx)
	stream.SetIncremental(true)
	_ = stream.SetSampleRate(24000)
	_ = stream.Write([]float32{.1})
	player := &previewPCMPlayer{fakePreviewPlayer: newFakePreviewPlayer(), called: make(chan struct{})}
	cleaned := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ensure := func(context.Context, *config.Config, func(string, float64)) error { return nil }
		factory := func(*config.Config) (tts.Provider, func(), error) {
			return previewStreamTTS{stream: stream, started: started}, func() { close(cleaned) }, nil
		}
		done <- runVoicePreview(ctx, tts.Voice{Name: "sample"}, player, &config.Config{}, ensure, factory)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop waited for synthesis to finish")
	}
	select {
	case <-player.called:
		t.Fatal("cancelled preview reached playback")
	default:
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("cancelled preview did not release the provider")
	}
	stream.Close()
}

func TestVoicePreviewCollectionHonorsStallBudget(t *testing.T) {
	stream := audio.NewPCMStream(t.Context())
	stream.SetStallBudget(20 * time.Millisecond)
	defer stream.Close()
	player := &previewPCMPlayer{fakePreviewPlayer: newFakePreviewPlayer(), called: make(chan struct{})}
	err := runPreviewWithStream(t.Context(), player, stream, nil)
	if !errors.Is(err, audio.ErrStreamStalled) {
		t.Fatalf("stalled preview = %v", err)
	}
	select {
	case <-player.called:
		t.Fatal("stalled preview reached playback")
	default:
	}
}

func runPreviewWithStream(ctx context.Context, player audio.Engine, stream *audio.PCMStream, cleanup func()) error {
	ensure := func(context.Context, *config.Config, func(string, float64)) error { return nil }
	factory := func(*config.Config) (tts.Provider, func(), error) {
		return previewStreamTTS{stream: stream}, cleanup, nil
	}
	return runVoicePreview(ctx, tts.Voice{Name: "sample"}, player, &config.Config{}, ensure, factory)
}

type previewStreamTTS struct {
	fakePreviewTTS
	stream  *audio.PCMStream
	started chan struct{}
}

func (p previewStreamTTS) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	if p.started != nil {
		close(p.started)
	}
	return p.stream, nil
}

type previewPCMPlayer struct {
	*fakePreviewPlayer
	called  chan struct{}
	rate    int
	samples []float32
}

func (p *previewPCMPlayer) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	close(p.called)
	var err error
	p.rate, err = stream.WaitReady(ctx)
	if err != nil {
		return nil, err
	}
	for chunk := range stream.Frames() {
		p.samples = append(p.samples, chunk...)
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return p.fakePreviewPlayer.PlayStream(ctx, stream)
}
