package voiceagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
)

func TestNativeCandidatePreparesOnlySelectedResourcesAndRetiresWithoutMediaLoss(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	var requests, asr, chat, speech atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/version":
			_, _ = io.WriteString(w, `{"version":"fixture"}`)
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "selected", "digest": strings.Repeat("a", 64)}}})
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["audio","speech","completion"]}`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"selected"}]}`)
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/v1/audio/transcriptions":
			asr.Add(1)
			t.Error("transcript off called ASR")
			w.WriteHeader(404)
		case "/api/chat":
			chat.Add(1)
			_, _ = io.WriteString(w, `{"model":"selected","message":{"role":"assistant","content":"15"},"done":true}`)
		case "/v1/audio/speech":
			speech.Add(1)
			var input struct{ Model, Input string }
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Model != "selected" || input.Input != "Ready." {
				t.Error(input)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "selected", TTSProvider: config.OpenAISpeechProviderName, TTSFallbackProvider: "disabled", TTSLookahead: 1, OpenAISpeechRuntime: "external", OpenAISpeechExternal: true, OpenAISpeechURL: server.URL + "/v1", OpenAISpeechModel: "selected", ModelsDir: t.TempDir(), WorkDir: t.TempDir(), SkillsEnabled: false}
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{.1, .2, .3})
	if err != nil {
		t.Fatal(err)
	}
	opts := NativeCandidateOptions{Lifetime: t.Context(), Media: media, Cache: nativeaudio.NewSupportCache(), Controls: "conversation", Transcript: "off"}
	candidate, err := PrepareNativeCandidate(t.Context(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Policy.Media != media || candidate.Voice.Fallback != nil || !candidate.Setup.ProtocolReady || candidate.Setup.QualityQualified || chat.Load() != 1 || speech.Load() != 1 || asr.Load() != 0 {
		t.Fatal("candidate substituted resources or claimed qualification")
	}
	cfg.OllamaModel = "later edit"
	if candidate.cfg.OllamaModel != "selected" {
		t.Fatal("candidate config was mutable")
	}
	if _, err := candidate.InputFactory(nil, nil); err == nil {
		t.Fatal("input accepted missing capture/VAD")
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := media.ResolveAudio(t.Context(), ref); err != nil {
		t.Fatal("candidate closed conversation media", err)
	}
	before := requests.Load()
	if _, err := candidate.Policy.Client.Metadata(t.Context(), false); err == nil {
		t.Fatal("retired client still usable")
	}
	if requests.Load() != before {
		t.Fatal("retired client reached selected server")
	}
	if _, err := candidate.InputFactory(nil, nil); err == nil {
		t.Fatal("closed candidate accepted reader")
	}
}

func TestNativeCandidateRejectsInvalidPolicyBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); t.Error("invalid policy made HTTP") }))
	defer server.Close()
	media := chatmedia.NewAudioSession()
	defer media.Close()
	cfg := &config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "selected", TTSProvider: config.OpenAISpeechProviderName, TTSFallbackProvider: "disabled", TTSLookahead: 1}
	good := NativeCandidateOptions{Lifetime: t.Context(), Media: media, Cache: nativeaudio.NewSupportCache(), Controls: "conversation", Transcript: "off"}
	for _, change := range []func(*NativeCandidateOptions){
		func(o *NativeCandidateOptions) { o.Controls = "invalid" },
		func(o *NativeCandidateOptions) { o.Controls = "standard" },
		func(o *NativeCandidateOptions) { o.Transcript = "auto" },
		func(o *NativeCandidateOptions) { o.RetainAudio = true },
		func(o *NativeCandidateOptions) { o.Lifetime = nil },
	} {
		opts := good
		change(&opts)
		if candidate, err := PrepareNativeCandidate(t.Context(), cfg, opts); err == nil || candidate != nil {
			t.Fatal("invalid policy accepted")
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	good.Lifetime = cancelled
	if _, err := PrepareNativeCandidate(t.Context(), cfg, good); err == nil {
		t.Fatal("cancelled lifetime accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid candidate touched HTTP")
	}
}
