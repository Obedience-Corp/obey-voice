//go:build !integration

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestPersonaQuietSetupChecksSavedDraftWithoutMutation(t *testing.T) {
	dir := personaEnv(t)
	var requests, asr atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"fixture-model","digest":"%s"}]}`, strings.Repeat("a", 64))
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","audio","speech"],"voices":["female"]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/api/chat":
			fmt.Fprint(w, `{"model":"fixture-model","message":{"role":"assistant","content":"15"},"done":true}`)
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0})
		case "/v1/audio/transcriptions":
			asr.Add(1)
			t.Error("off draft started ASR")
			w.WriteHeader(404)
		default:
			t.Error("unexpected setup route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Audio = &persona.Audio{Mode: persona.AudioStandard, Model: "fixture-model", Endpoint: server.URL, Voice: "female", Controls: persona.AudioControlsConversation, Transcript: persona.AudioTranscriptOff}
	writePersona(t, p)
	global, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := personaAudioCheckCmd()
	cmd.SetContext(t.Context())
	result, err := checkPersonaAudio(cmd, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	if !result.ProtocolReady || result.QualityQualified || result.Requirements.ASR || result.Requirements.Tools || result.SpeechSamples != 2 || asr.Load() != 0 {
		t.Fatalf("wrong setup status: %+v", result)
	}
	saved, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Audio.Mode != persona.AudioStandard {
		t.Fatal("check activated saved draft")
	}
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || !bytes.Equal(global, after) {
		t.Fatal("setup changed global config", err)
	}
	before := requests.Load()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd.SetContext(ctx)
	if _, err := checkPersonaAudio(cmd, "samantha"); err == nil {
		t.Fatal("accepted canceled setup")
	}
	if requests.Load() != before {
		t.Fatal("canceled setup made network requests")
	}
}
