package nativeaudio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func TestTranscriptionUsesOnlySelectedModelAndExactWAV(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := media.ResolveAudio(t.Context(), ref)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/v1/audio/transcriptions" {
			t.Errorf("route %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("borrowed credentials")
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("model") != "chosen-model" {
			t.Error("model changed")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, expected) || header.Header.Get("Content-Type") != "audio/wav" {
			t.Error("invalid audio transport")
		}
		_, _ = io.WriteString(w, `{"text":" actual words "}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL+"/prefix/", "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	got, err := client.Transcribe(t.Context(), ref, media)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "actual words" || got.Model != "chosen-model" || got.Provider != "ollama" {
		t.Fatal(got)
	}
}
func TestTranscriptionRejectsRedirectAndCancelledDispatch(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	var remote, local atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { remote.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local.Add(1)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "chosen-model")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Transcribe(t.Context(), ref, media); err == nil {
		t.Fatal("followed redirect")
	}
	if remote.Load() != 0 {
		t.Fatal("audio reached redirect destination")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Transcribe(ctx, ref, media); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if local.Load() != 1 {
		t.Fatal("cancelled request reached server")
	}
}
func TestTranscriptionRejectsEmptyMalformedAndOversizedResponses(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"text":""}`, `broken`, string(bytes.Repeat([]byte{'x'}, MaxTranscriptBytes+1))} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, payload) }))
		client, err := NewClient(server.URL, "chosen-model")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Transcribe(t.Context(), ref, media); err == nil {
			t.Fatal("accepted invalid transcription")
		}
		client.Close()
		server.Close()
	}
}

func TestNativeBindingRejectsInconsistentAmbientOllamaAuthentication(t *testing.T) {
	t.Setenv("OLLAMA_AUTH", "1")
	if client, err := NewClient("http://localhost:11434", "chosen-model"); err == nil {
		client.Close()
		t.Fatal("accepted auth available to chat only")
	}
}
