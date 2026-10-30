//go:build !integration

package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audiojob"
)

func TestAudioJobCanonicalizesOutboundOriginAndPreservesProvenance(t *testing.T) {
	for _, suffix := range []string{"", "/"} {
		t.Run(map[string]string{"": "origin", "/": "root_slash"}[suffix], func(t *testing.T) {
			var mu sync.Mutex
			paths := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths[r.Method+" "+r.URL.Path]++
				mu.Unlock()
				switch r.Method + " " + r.URL.Path {
				case "GET /api/version":
					_ = json.NewEncoder(w).Encode(map[string]string{"version": "fixture-runtime"})
				case "GET /api/tags":
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture-audio", "digest": strings.Repeat("a", 64)}}})
				case "GET /v1/models":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "fixture-audio"}}})
				case "GET /v1/audio/voices":
					_ = json.NewEncoder(w).Encode(map[string]any{"voices": []string{"female"}})
				case "POST /v1/audio/speech":
					var request map[string]any
					if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
						t.Error(e)
					}
					if request["model"] != "fixture-audio" || request["voice"] != "female" || request["response_format"] != "pcm" {
						t.Errorf("unexpected synthesis request: %+v", request)
					}
					_, _ = w.Write(make([]byte, 480)) // Nonempty quiet PCM is valid.
				default:
					t.Errorf("unexpected outbound path %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			root := t.TempDir()
			input := filepath.Join(root, "source.wav")
			if e := audio.WriteWAVFloat32(input, 16000, make([]float32, 160)); e != nil {
				t.Fatal(e)
			}
			wav, e := os.ReadFile(input)
			if e != nil {
				t.Fatal(e)
			}
			digest := sha256.Sum256(wav)
			runtime := audiojob.Runtime{Provider: "ollama", Endpoint: server.URL + suffix, Model: "fixture-audio", Voice: "female"}
			config := audiojob.Config{Schema: audiojob.ConfigSchema, Runtime: runtime, StorageRoot: root}
			req := audiojob.Request{Protocol: audiojob.Protocol, JobID: "canonical-origin", Task: "narration", Source: audiojob.Source{MediaSHA256: strings.Repeat("1", 64), ClipID: "clip", RangeID: "range", ProjectRevision: "revision"}, Input: audiojob.Input{Path: input, SHA256: hex.EncodeToString(digest[:]), SampleCount: 160, SampleRate: 16000, Channels: 1, ChannelRole: "microphone"}, Runtime: runtime, Script: "Quiet fixture narration.", Language: "en", DurationPolicy: "preserve_picture", OutputDir: filepath.Join(root, "job"), Limits: audiojob.Limits{TimeoutSeconds: 10, OutputBytes: 1 << 20}}
			write := func(name string, value any) string {
				t.Helper()
				path := filepath.Join(root, name)
				data, e := json.Marshal(value)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, data, 0600); e != nil {
					t.Fatal(e)
				}
				return path
			}
			configPath, requestPath := write("config.json", config), write("request.json", req)
			command := newAudioJobCmd()
			var events bytes.Buffer
			command.SetOut(&events)
			command.SetErr(&events)
			command.SetArgs([]string{"--audio-config", configPath, "--request", requestPath, "--jsonl"})
			if e = command.Execute(); e != nil {
				t.Fatalf("command: %v\n%s", e, events.String())
			}
			var manifest audiojob.Manifest
			data, e := os.ReadFile(filepath.Join(req.OutputDir, "manifest.json"))
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(data, &manifest); e != nil {
				t.Fatal(e)
			}
			if manifest.Runtime != runtime {
				t.Fatalf("runtime provenance changed: %+v", manifest.Runtime)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, path := range []string{"GET /api/version", "GET /api/tags", "GET /v1/models", "GET /v1/audio/voices", "POST /v1/audio/speech"} {
				if paths[path] != 1 {
					t.Errorf("path %s requests=%d; all=%+v", path, paths[path], paths)
				}
			}
		})
	}
}
