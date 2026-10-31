package audiojob

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a runtime fixture, so process death, cancellation
// and environment isolation exercise an actual executable rather than a mock.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "native-waveform-fixture" {
		runWaveformFixture()
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("version: 0.8.0-dev (build 11960, commit 8642541b2)")
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[1] == "--model" {
		runManagedFixture()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runManagedFixture() {
	args := map[string]string{}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--offline" || os.Args[i] == "--no-webui" {
			continue
		}
		if i+1 < len(os.Args) {
			args[os.Args[i]] = os.Args[i+1]
			i++
		}
	}
	mode, _ := os.ReadFile(args["--model"])
	if os.Getenv("LLAMA_ARG_MODEL") != "" || os.Getenv("HTTP_PROXY") != "" || os.Getenv("OLLAMA_HOST") != "" {
		os.Exit(2)
	}
	if string(mode) == "crash" {
		os.Exit(3)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+args["--api-key"] {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			if string(mode) == "unready" {
				w.WriteHeader(503)
				return
			}
			input := []string{"text", "audio"}
			if string(mode) == "no-audio-input" {
				input = []string{"text"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": ManagedModel, "architecture": map[string]any{"input_modalities": input, "output_modalities": []string{"text", "audio"}}}}})
		case "/tts":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["input"] != "Approved words." || req["voice"] != "female" || req["response_format"] != "pcm" || req["lang"] != "en" || req["seed"] != float64(42) {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "audio/pcm; rate=24000; encoding=float; bits=32")
			switch string(mode) {
			case "hang":
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			case "partial":
				_, _ = w.Write([]byte{1, 2, 3})
			case "truncated":
				w.Header().Set("Content-Length", "2000")
				_, _ = w.Write(make([]byte, 960))
			case "nan":
				_ = binary.Write(w, binary.LittleEndian, float32(math.NaN()))
			case "bad-format":
				w.Header().Set("Content-Type", "audio/pcm; rate=16000; encoding=float; bits=32")
				_, _ = w.Write(make([]byte, 960))
			default:
				_ = binary.Write(w, binary.LittleEndian, make([]float32, 240))
			}
		default:
			w.WriteHeader(404)
		}
	})
	_ = http.ListenAndServe("127.0.0.1:"+args["--port"], handler)
}

func managedFixture(t *testing.T, mode string) (Config, Request) {
	t.Helper()
	c, r := fixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file := func(path string) ManagedFile {
		hash, err := fileHash(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		return ManagedFile{Path: path, SHA256: hash}
	}
	model, projector := filepath.Join(c.StorageRoot, "model"), filepath.Join(c.StorageRoot, "projector")
	if err = os.WriteFile(model, []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(projector, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Runtime = Runtime{Provider: "llama.cpp", Model: ManagedModel, Voice: "female"}
	c.Managed = &ManagedConfig{Executable: file(executable), Model: file(model), Projector: file(projector), Version: ManagedVersion}
	r.Runtime = c.Runtime
	return c, r
}

func TestAudioManagedRuntimePublishesAndReaps(t *testing.T) {
	t.Setenv("LLAMA_ARG_MODEL", "must-not-be-used")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("OLLAMA_HOST", "https://example.invalid")
	c, r := managedFixture(t, "healthy")
	p, provenance, err := StartManaged(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	manifest, err := Run(t.Context(), c, r, p, provenance, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Output.SampleCount != 240 || manifest.ProjectorDigest != c.Managed.Projector.SHA256 || manifest.RuntimeDigest != c.Managed.Executable.SHA256 || manifest.Sampling.Seed != 42 || manifest.Runtime.Endpoint != "" {
		t.Fatalf("missing managed provenance: %+v", manifest)
	}
	p.Close()
	if p.cmd.ProcessState == nil {
		t.Fatal("owned child was not waited")
	}
	if _, err = os.Stat(p.scratch); !os.IsNotExist(err) {
		t.Fatal("runtime scratch survived close")
	}
}

func TestAudioManagedFailuresLeaveNoArtifact(t *testing.T) {
	for _, mode := range []string{"partial", "truncated", "nan", "bad-format", "hang"} {
		t.Run(mode, func(t *testing.T) {
			c, r := managedFixture(t, mode)
			p, provenance, err := StartManaged(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			if _, err = Run(ctx, c, r, p, provenance, func(Event) error { return nil }); err == nil {
				t.Fatal("invalid speech was accepted")
			}
			if _, err = os.Stat(r.OutputDir); !os.IsNotExist(err) {
				t.Fatal("failed speech was published")
			}
			p.Close()
			matches, _ := filepath.Glob(filepath.Join(c.StorageRoot, ".audio-*"))
			if len(matches) != 0 {
				t.Fatal("private scratch leaked", matches)
			}
		})
	}
}

func TestAudioManagedReadinessAndChangedAssets(t *testing.T) {
	for _, mode := range []string{"unready", "crash", "changed", "version", "remote", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := managedFixture(t, mode)
			switch mode {
			case "changed":
				c.Managed.Model.SHA256 = strings.Repeat("0", 64)
			case "version":
				c.Managed.Version = "unreviewed"
			case "remote":
				c.Runtime.Endpoint = "http://example.invalid"
			case "mixed":
				c.Runtime.Provider = "ollama"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			p, _, err := StartManaged(ctx, c)
			if p != nil || err == nil {
				p.Close()
				t.Fatal("bad setup unexpectedly started")
			}
			matches, _ := filepath.Glob(filepath.Join(c.StorageRoot, ".audio-runtime-*"))
			if len(matches) != 0 {
				t.Fatal("readiness failure leaked scratch", matches)
			}
		})
	}
}
