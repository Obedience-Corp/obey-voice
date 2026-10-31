package stt

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestNativeSessionCloseReleasesAbandonedFinalizedAudio(t *testing.T) {
	for _, cancelBeforeBridge := range []bool{false, true} {
		media := chatmedia.NewAudioSession()
		ctx, cancel := context.WithCancel(context.Background())
		ref, err := media.ImportSamples(ctx, []float32{0.1, 0.2})
		if err != nil {
			t.Fatal(err)
		}
		base := &sherpaSession{cancel: cancel, events: make(chan Event, 2), done: make(chan struct{})}
		session := &nativeSession{sherpaSession: base, events: make(chan utterance.Event, 2), bridgeDone: make(chan struct{}), media: media}
		base.events <- nativeFinalized{input: &utterance.Finalized{Audio: &ref}, release: func() { media.Release(ref) }}
		close(base.events)
		close(base.done)
		if cancelBeforeBridge {
			cancel()
		}
		go session.bridge(ctx)
		<-session.bridgeDone
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := media.ResolveAudio(context.Background(), ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
			t.Fatalf("abandoned audio retained: %v", err)
		}
		media.Close()
	}
}
func TestNativeSessionDeliveredAudioTransfersToConsumer(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ref, err := media.ImportSamples(ctx, []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	base := &sherpaSession{cancel: cancel, events: make(chan Event, 2), done: make(chan struct{})}
	session := &nativeSession{sherpaSession: base, events: make(chan utterance.Event, 2), bridgeDone: make(chan struct{}), media: media}
	base.events <- nativeFinalized{input: &utterance.Finalized{Audio: &ref}, release: func() { media.Release(ref) }}
	close(base.events)
	close(base.done)
	go session.bridge(ctx)
	event := <-session.Events()
	if event.Kind != utterance.FinalizedEvent {
		t.Fatal(event)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := media.ResolveAudio(context.Background(), ref); err != nil {
		t.Fatal("consumer ref released", err)
	}
	media.Release(ref)
}
