package nativeaudio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type setupFixture struct {
	server                                  *httptest.Server
	version, digest                         atomic.Value
	missingASR, missingSpeech, missingAudio atomic.Bool
	requests, asr, chat, speech             atomic.Int32
}

func newSetupFixture(t *testing.T) *setupFixture {
	t.Helper()
	f := &setupFixture{}
	f.version.Store("fixture-runtime")
	f.digest.Store(strings.Repeat("a", 64))
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": f.version.Load()})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "chosen-model", "digest": f.digest.Load()}}})
		case "/api/show":
			caps := []string{"completion", "speech", "tools"}
			if !f.missingAudio.Load() {
				caps = append(caps, "audio")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps, "voices": []string{"female", "male"}})
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "chosen-model"}}})
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/v1/audio/transcriptions":
			f.asr.Add(1)
			if f.missingASR.Load() {
				w.WriteHeader(404)
				return
			}
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.FormValue("model") != "chosen-model" {
				t.Error("ASR model changed")
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != string(setupAudio) {
				t.Error("setup recording changed", err)
			}
			_, _ = io.WriteString(w, `{"text":"What is seven plus eight? Reply with only the answer."}`)
		case "/api/chat":
			f.chat.Add(1)
			var request struct {
				Model    string `json:"model"`
				Messages []struct {
					Role, Content string
					Images        [][]byte
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Model != "chosen-model" || len(request.Messages) != 2 || request.Messages[1].Content != "" || len(request.Messages[1].Images) != 1 || string(request.Messages[1].Images[0]) != string(setupAudio) {
				t.Error("setup chat did not use selected audio/model")
			}
			_, _ = io.WriteString(w, `{"model":"chosen-model","message":{"role":"assistant","content":"15"},"done":true}`)
		case "/v1/audio/speech":
			f.speech.Add(1)
			if f.missingSpeech.Load() {
				w.WriteHeader(404)
				return
			}
			var request struct{ Model, Input, Voice string }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Model != "chosen-model" || request.Input != "Ready." || (request.Voice != "female" && request.Voice != "male") {
				t.Error("speech model/voice/input changed", request)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *setupFixture) config(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{OpenAISpeechRuntime: "external", OpenAISpeechExternal: true, OpenAISpeechURL: f.server.URL + "/v1", OpenAISpeechModel: "chosen-model", OpenAISpeechVoice: "female", ModelsDir: t.TempDir()}
}
func TestSetupCacheTracksIdentityAndRevalidatesVanishedRoutes(t *testing.T) {
	f := newSetupFixture(t)
	client, err := NewClient(f.server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cache := NewSupportCache()
	cfg := f.config(t)
	required := Requirements{ASR: true, Tools: true}
	prepare := func(known bool) {
		t.Helper()
		speech, closeSpeech, result, err := cache.Prepare(t.Context(), client, cfg, required)
		if err != nil {
			t.Fatal(err)
		}
		defer closeSpeech()
		if speech == nil || !result.ProtocolReady || result.QualityQualified || result.KnownCombination != known || result.Transcript == nil || result.SpeechSamples != 2 {
			t.Fatal(result)
		}
	}
	prepare(false)
	prepare(true)
	if f.asr.Load() != 2 || f.chat.Load() != 2 || f.speech.Load() != 2 {
		t.Fatal("activation skipped route validation")
	}
	f.missingASR.Store(true)
	if _, _, _, err := cache.Prepare(t.Context(), client, cfg, required); err == nil {
		t.Fatal("cached support hid vanished ASR route")
	}
	if f.chat.Load() != 2 || f.speech.Load() != 2 {
		t.Fatal("ASR failure continued to chat/speech")
	}
	f.missingASR.Store(false)
	prepare(false)
	f.version.Store("updated-runtime")
	prepare(false)
	f.digest.Store(strings.Repeat("b", 64))
	prepare(false)
	cfg.OpenAISpeechVoice = "male"
	prepare(false)
}
func TestSetupOffDoesNotProbeASRAndMissingCapabilityFailsBeforeInference(t *testing.T) {
	f := newSetupFixture(t)
	f.missingASR.Store(true)
	client, err := NewClient(f.server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cache := NewSupportCache()
	cfg := f.config(t)
	_, closeSpeech, result, err := cache.Prepare(t.Context(), client, cfg, Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	closeSpeech()
	if f.asr.Load() != 0 || result.Transcript != nil {
		t.Fatal("transcript off invoked ASR")
	}
	f.missingAudio.Store(true)
	chat, speech := f.chat.Load(), f.speech.Load()
	if _, _, _, err := cache.Prepare(t.Context(), client, cfg, Requirements{}); err == nil {
		t.Fatal("accepted missing audio capability")
	}
	if f.chat.Load() != chat || f.speech.Load() != speech {
		t.Fatal("missing capability performed inference")
	}
}
func TestSetupRejectsMismatchedSpeechAndCancellationBeforeDispatch(t *testing.T) {
	f := newSetupFixture(t)
	client, err := NewClient(f.server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cache := NewSupportCache()
	cfg := f.config(t)
	cfg.OpenAISpeechModel = "unrelated-model"
	if _, _, _, err := cache.Prepare(t.Context(), client, cfg, Requirements{}); err == nil {
		t.Fatal("mismatched speech accepted")
	}
	cfg.OpenAISpeechModel = "chosen-model"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := cache.Prepare(ctx, client, cfg, Requirements{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("rejected setup made HTTP requests")
	}
}
func TestSetupMetadataHonorsParentDeadline(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(stopped) }))
	defer server.Close()
	client, err := NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := client.Metadata(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("metadata request did not close")
	}
}

func TestSetupCacheSeparatesEndpointsAndRejectsChangedUnsupportedRoute(t *testing.T) {
	first, second := newSetupFixture(t), newSetupFixture(t)
	cache := NewSupportCache()
	prepare := func(f *setupFixture) (SetupResult, error) {
		t.Helper()
		client, err := NewClient(f.server.URL, "chosen-model")
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		_, closeSpeech, result, err := cache.Prepare(t.Context(), client, f.config(t), Requirements{ASR: true})
		if closeSpeech != nil {
			closeSpeech()
		}
		return result, err
	}
	if result, err := prepare(first); err != nil || !result.ProtocolReady || result.KnownCombination {
		t.Fatalf("initial endpoint: result=%+v error=%v", result, err)
	}
	second.missingASR.Store(true)
	result, err := prepare(second)
	if err == nil || result.ProtocolReady || result.KnownCombination {
		t.Fatalf("changed unsupported endpoint inherited support: result=%+v error=%v", result, err)
	}
	if second.asr.Load() != 1 || second.chat.Load() != 0 || second.speech.Load() != 0 {
		t.Fatal("failed endpoint continued inference or skipped required ASR")
	}
	second.missingASR.Store(false)
	if result, err := prepare(second); err != nil || !result.ProtocolReady || result.KnownCombination {
		t.Fatalf("repaired endpoint reused another endpoint's evidence: result=%+v error=%v", result, err)
	}
	if result, err := prepare(first); err != nil || !result.ProtocolReady || !result.KnownCombination {
		t.Fatalf("original endpoint lost independent support: result=%+v error=%v", result, err)
	}
	if first.asr.Load() != 2 || first.chat.Load() != 2 || first.speech.Load() != 2 || second.asr.Load() != 2 || second.chat.Load() != 1 || second.speech.Load() != 1 {
		t.Fatal("cache altered quiet route revalidation counts")
	}
}
