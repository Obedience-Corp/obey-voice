package openaispeech

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestSpeechInferenceHealthRequiresCompletedPrimaryRequest(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "phonon"}}})
			return
		}
		if r.URL.Path != "/v1/audio/speech" {
			http.NotFound(w, r)
			return
		}
		if !healthy.Load() {
			http.Error(w, "worker exited", 500)
			return
		}
		body := make([]byte, 480)
		for i := 0; i < len(body); i += 2 {
			binary.LittleEndian.PutUint16(body[i:], 3000)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	p, closeProvider, err := New(&config.Config{OpenAISpeechURL: srv.URL + "/v1", OpenAISpeechModel: "phonon", OpenAISpeechRuntime: "external", OpenAISpeechVoice: "Zoey"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeProvider()
	var mu sync.Mutex
	var states []string
	p.SetProviderStatusSink(func(_ context.Context, info events.Info) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, info.ProviderStatus.State)
	})
	for _, recovery := range []bool{false, true} {
		healthy.Store(recovery)
		stream, err := p.Synthesize(t.Context(), "Hello.")
		if err != nil {
			t.Fatal(err)
		}
		for range stream.Frames() {
		}
		if (stream.Err() == nil) != recovery {
			t.Fatalf("recovery=%v error=%v", recovery, stream.Err())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(states, []string{"unavailable", "available"}) {
		t.Fatalf("states=%v", states)
	}
}
