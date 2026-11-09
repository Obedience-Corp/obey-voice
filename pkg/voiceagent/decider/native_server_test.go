package decider

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeAdapterWorkerFixture(t *testing.T) {
	enabled := false
	socket := ""
	for i, arg := range os.Args {
		if arg == "native-adapter-fixture" {
			enabled = true
		}
		if arg == "--host" && i+1 < len(os.Args) {
			socket = os.Args[i+1]
		}
	}
	if !enabled {
		return
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		var input struct {
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answers := map[string]any{}
		for key := range input.Questions {
			answers[key] = map[string]any{"noul": 1.0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	})
	if err := http.Serve(listener, handler); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAdapterStartupAndClose(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := RuntimeDir(root, "cpu")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Run the native Go test fixture through the production supervisor and its
	// inherited descriptors, including listener mode changes during exec.
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestNativeAdapterWorkerFixture$ -- native-adapter-fixture \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "llama-server"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	server, err := startNative(ctx, root, "unused", Model{Name: "fixture", Engine: "liquid"}, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(strings.TrimSuffix(server.Endpoint(), "/v1/systemone") + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	done := make(chan struct{})
	go func() { server.Close(); server.Close(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("native adapter shutdown blocked")
	}
	if _, err := http.Get(server.Endpoint()); err == nil {
		t.Fatal("closed adapter still reachable")
	}
}

func TestNativeLoaderFailureDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.log")
	if err := os.WriteFile(path, []byte("llama-server: libgomp.so.1: error while loading shared libraries\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := nativeStartupError(path, context.Canceled)
	if !strings.Contains(err.Error(), "libgomp installed") || !strings.Contains(err.Error(), "external local server") {
		t.Fatalf("not actionable: %v", err)
	}
}
