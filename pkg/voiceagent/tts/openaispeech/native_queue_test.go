package openaispeech

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func TestNativeCleanupBusyResponseIsRetriedBeforeAudio(t *testing.T) {
	f := &fakeServer{t: t, models: []string{"phonon"}, voices: []string{"default"}, chunks: []int{4096}, samples: 2400}
	inner := f.handler()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/speech" && attempts.Add(1) == 1 {
			http.Error(w, "server_busy", http.StatusTooManyRequests)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p, cleanup, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p.nativeSlot = make(chan struct{}, 1)
	if _, err := drain(t, p, "Hello"); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("got %d requests", attempts.Load())
	}
}

func TestNativeQueueWaitHonorsCancellation(t *testing.T) {
	f, srv := newFake(t)
	p, cleanup, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p.nativeSlot = make(chan struct{}, 1)
	p.nativeSlot <- struct{}{} // A previous synthesis still owns the server.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := p.Synthesize(ctx, "A cancelled queued request")
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Frames() {
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Fatalf("got %v", stream.Err())
	}
	var providerErr *tts.ProviderError
	if errors.As(stream.Err(), &providerErr) && providerErr.Kind != tts.ProviderErrorCanceled {
		t.Fatalf("wrong cancellation kind: %v", stream.Err())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("cancelled queue entry reached the server")
	}
}
