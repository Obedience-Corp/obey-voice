package pipeline

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
)

func TestBackchannelClipsFromOpenAISpeechProvider(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"speech-test"}]}`)
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ResponseFormat string `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ResponseFormat != "pcm" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body := make([]byte, 2*2400)
		for i := range 2400 {
			binary.LittleEndian.PutUint16(body[2*i:], uint16(int16(i%2000-1000)))
		}
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	provider, cleanup, err := openaispeech.New(&config.Config{
		TTSProvider:         config.OpenAISpeechProviderName,
		OpenAISpeechRuntime: "external",
		OpenAISpeechURL:     srv.URL + "/v1",
		OpenAISpeechModel:   "speech-test",
		OpenAISpeechVoice:   "test-voice",
		ModelsDir:           t.TempDir(),
	})
	if err != nil {
		t.Fatalf("openaispeech.New: %v", err)
	}
	t.Cleanup(cleanup)

	p := &Pipeline{TTS: provider}
	p.EnableBackchannel(t.Context())

	p.backchannelMu.Lock()
	clips := append([]backchannelClip(nil), p.backchannel.clips...)
	p.backchannelMu.Unlock()
	if len(clips) != len(backchannelPhrases) {
		t.Fatalf("clips = %d, want %d", len(clips), len(backchannelPhrases))
	}
	for _, clip := range clips {
		if clip.rate <= 0 || len(clip.samples) == 0 {
			t.Fatalf("clip %q: rate %d, %d samples", clip.phrase, clip.rate, len(clip.samples))
		}
	}
}
