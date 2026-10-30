//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audiojob"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
)

func TestAudioRuntimeMetadataNeverDownloads(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "test-runtime"})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "installed", "digest": strings.Repeat("a", 64)}}})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p, e := audioRuntimeProvenance(context.Background(), audiojob.Runtime{Endpoint: server.URL, Model: "installed"})
	if e != nil || p.RuntimeVersion != "test-runtime" {
		t.Fatalf("%+v %v", p, e)
	}
	_, e = audioRuntimeProvenance(context.Background(), audiojob.Runtime{Endpoint: server.URL, Model: "missing"})
	if e == nil {
		t.Fatal("missing model accepted")
	}
	if len(paths) != 4 {
		t.Fatal(paths)
	}
}
func TestAudioJobRequiresIsolatedFlags(t *testing.T) {
	cmd := newAudioJobCmd()
	cmd.SetArgs([]string{"--jsonl"})
	if e := cmd.Execute(); e == nil {
		t.Fatal("missing config accepted")
	}
}

func TestLocalAudioRejectsRedirectAndInheritedProxy(t *testing.T) {
	var received int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received++; w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	_, e := audioRuntimeProvenance(context.Background(), audiojob.Runtime{Endpoint: source.URL, Model: "model"})
	if e == nil || received != 0 {
		t.Fatalf("redirect reached other origin: %v count %d", e, received)
	}
	client := localAudioHTTPClient()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("inherited proxy enabled")
	}
}

func TestAudioProviderConstructionHonorsCancellationAndRedirects(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "redirect", true: "cancelled"}[cancelled], func(t *testing.T) {
			var probes, forwarded int
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++; w.WriteHeader(200) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes++
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer origin.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			_, close, e := openaispeech.NewWithContext(ctx, &config.Config{TTSProvider: "openai-speech", OpenAISpeechRuntime: "external", OpenAISpeechURL: origin.URL + "/v1", OpenAISpeechModel: "model"}, localAudioHTTPClient())
			if close != nil {
				close()
			}
			if e == nil || forwarded != 0 || (cancelled && probes != 0) {
				t.Fatalf("unexpected setup e=%v probes=%d forwarded=%d", e, probes, forwarded)
			}
		})
	}
}
