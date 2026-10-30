package phonon

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This helper runs as an actual child process: lifecycle checks exercise the
// same exec/readiness/cleanup boundary used by the native Rust runtime.
func TestRuntimeChild(t *testing.T) {
	if os.Getenv("PHONON_TEST_CHILD") != "1" {
		return
	}
	arg := func(name string) string {
		for i, a := range os.Args {
			if a == name && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return ""
	}
	if os.Getenv("PTTS_QUANT") != "" {
		os.Exit(3)
	}
	if arg("--model") != "hf://"+Model+"@"+ModelRevision {
		os.Exit(4)
	}
	if !strings.Contains(arg("--voice-catalog"), CatalogRevision) {
		os.Exit(5)
	}
	addr := arg("--addr")
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		os.Exit(6)
	}
	if marker := os.Getenv("PHONON_TEST_ADDRESS"); marker != "" {
		_ = os.WriteFile(marker, []byte(addr), 0600)
	}
	fmt.Println("model loaded device=CPU weights=f32")
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		model := Model
		if os.Getenv("PHONON_TEST_NOT_READY") == "1" {
			model = "different-model"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": model}}})
	})
	mux.HandleFunc("/v1/audio/voices", func(w http.ResponseWriter, r *http.Request) {
		voices := namedVoices
		if os.Getenv("PHONON_TEST_MISSING_VOICE") == "1" {
			voices = []string{"default"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"voices": voices})
	})
	_ = http.Serve(l, mux)
	os.Exit(0)
}

func childOptions(t *testing.T) Options {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell child fixture requires POSIX")
	}
	t.Setenv("PHONON_TEST_CHILD", "1")
	// The application's launcher must exclude conflicting upstream env flags.
	t.Setenv("PTTS_QUANT", "q8")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "server")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(bin)+" -test.run=^TestRuntimeChild$ -- \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return Options{ModelsDir: dir, Backend: "cpu", Binary: script}
}

func TestStartOwnsAndClosesChild(t *testing.T) {
	o := childOptions(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Start(ctx, o, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if p.Device != "CPU" || !p.Running() {
		t.Fatalf("device=%q running=%v", p.Device, p.Running())
	}
	if err := p.checkVoices(ctx, namedVoices); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close()
	if p.Running() {
		t.Fatal("child still running after cleanup")
	}
	if _, err := (&http.Client{Timeout: time.Second}).Get(p.URL + "/models"); err == nil {
		t.Fatal("child still serves requests after cleanup")
	}
}

func TestReadinessCancellationKillsChild(t *testing.T) {
	o := childOptions(t)
	t.Setenv("PHONON_TEST_NOT_READY", "1")
	marker := filepath.Join(t.TempDir(), "address")
	t.Setenv("PHONON_TEST_ADDRESS", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Start(ctx, o, false); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("got %v", err)
	}
	addr, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", string(addr), time.Second); err == nil {
		conn.Close()
		t.Fatal("cancelled child still listens")
	}
}

func TestStartRejectsSilentDeviceFallback(t *testing.T) {
	o := childOptions(t)
	o.Backend = "vulkan" // The fixture reports CPU, as an unavailable GPU would.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Start(ctx, o, true); err == nil || !strings.Contains(err.Error(), "refusing silent backend fallback") {
		t.Fatalf("got %v", err)
	}
}

func cacheFixture(t *testing.T, o Options) {
	t.Helper()
	model := filepath.Join(o.cache(), "hub", "models--gradium--phonon-31m-en", "snapshots", ModelRevision)
	voices := filepath.Join(o.cache(), "hub", "models--gradium--phonon-voices", "snapshots", CatalogRevision)
	for dir, files := range map[string][]string{
		model:  {"config.json", "tokenizer.json", "model.safetensors", "default-voice.safetensors"},
		voices: {"voices.jsonl", "voices/4SZHfMpw-p46Ywgs.safetensors", "voices/NIWIOv5vTi5eRMj8.safetensors", "voices/zUCxzrOrugTSis03.safetensors", "voices/NbpkqMVS3CJeq2j8.safetensors"},
	} {
		for _, file := range files {
			p := filepath.Join(dir, file)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("cached"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestEnsureVerifiesVoicesAndCacheBeforeMarkingReady(t *testing.T) {
	o := childOptions(t)
	cacheFixture(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Setenv("PHONON_TEST_MISSING_VOICE", "1")
	if err := Ensure(ctx, o, nil); err == nil || !strings.Contains(err.Error(), "Harper") {
		t.Fatalf("got %v", err)
	}
	if Installed(o) {
		t.Fatal("missing voices reported ready")
	}
	t.Setenv("PHONON_TEST_MISSING_VOICE", "")
	if err := Ensure(ctx, o, nil); err != nil {
		t.Fatal(err)
	}
	if !Installed(o) {
		t.Fatal("complete cache not ready")
	}
	if err := os.Remove(filepath.Join(o.cache(), "hub", "models--gradium--phonon-31m-en", "snapshots", ModelRevision, "model.safetensors")); err != nil {
		t.Fatal(err)
	}
	if Installed(o) {
		t.Fatal("removed model still reported ready")
	}
}

func TestBuildRejectsUnverifiedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX cargo fixture")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "cargo"), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("wrong source")) }))
	defer srv.Close()
	original := sourceURL
	sourceURL = srv.URL
	t.Cleanup(func() { sourceURL = original })
	o := Options{ModelsDir: t.TempDir(), Backend: "cpu"}
	if err := os.MkdirAll(o.root(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := build(context.Background(), o); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(o.binary()); !os.IsNotExist(err) {
		t.Fatalf("unverified binary installed: %v", err)
	}
}

func TestExtractAcceptsGitHubPAXMetadata(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "source.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": SourceRevision}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "source/Cargo.toml", Mode: 0644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("ok"))
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	if err := extract(archive, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "source/Cargo.toml")); err != nil || string(b) != "ok" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestExtractRejectsEscapingPathsAndSymlinks(t *testing.T) {
	for _, h := range []*tar.Header{{Name: "../escape", Mode: 0644, Size: 1}, {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}} {
		t.Run(h.Name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "source.gz")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				_, _ = tw.Write([]byte("x"))
			}
			_ = tw.Close()
			_ = gz.Close()
			_ = f.Close()
			if err := extract(archive, filepath.Join(dir, "dest")); err == nil {
				t.Fatal("unsafe entry extracted")
			}
		})
	}
}
