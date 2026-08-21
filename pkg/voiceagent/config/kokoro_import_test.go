package config

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// kokoroFrontend lays out the multi-lang frontend files the v1 import needs
// before it will attempt anything.
func kokoroFrontend(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tokens.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "espeak-ng-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// A converter that cannot run must cost nothing: no stage directory, no
// download. Before the pre-check, every serve start fetched ~350 MB and threw
// it away when the convert failed for a missing numpy.
func TestEnsureKokoroV1EnglishPackPreCheckDownloadsNothing(t *testing.T) {
	tests := []struct {
		name    string
		env     kokoroImportEnv
		wantErr string
	}{
		{
			name: "no interpreter on PATH",
			env: kokoroImportEnv{
				lookupPython: func() (string, error) { return "", errors.New("python3 not found on PATH") },
				probe:        func(context.Context, string) error { return errors.New("probe must not run without an interpreter") },
			},
			wantErr: "python3 not found on PATH",
		},
		{
			name: "interpreter without numpy",
			env: kokoroImportEnv{
				lookupPython: func() (string, error) { return "/usr/bin/python3", nil },
				probe: func(context.Context, string) error {
					return errors.New("python3 with numpy+onnx not available (ModuleNotFoundError: No module named 'numpy'); install with: pip3 install numpy onnx")
				},
			},
			wantErr: "pip3 install numpy onnx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := kokoroFrontend(t)
			env := tt.env
			env.fetch = func(context.Context, string, string, string, string, func(string, float64)) error {
				t.Fatal("fetch called after a failed pre-check")
				return nil
			}

			err := ensureKokoroV1EnglishPack(t.Context(), root, env, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, kokoroV1DownloadsSubdir)); !os.IsNotExist(statErr) {
				t.Errorf("downloads dir created despite a failed pre-check (stat err = %v)", statErr)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Errorf("root gained entries: %v", entries)
			}
		})
	}
}

// The pre-check passes and the weights are already staged, so the fetch seam
// reuses them: a retry after a failed convert must not download again.
func TestEnsureKokoroV1EnglishPackReusesStagedWeights(t *testing.T) {
	root := kokoroFrontend(t)
	stage := filepath.Join(root, kokoroV1DownloadsSubdir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{kokoroV1OnnxName, kokoroV1VoicesName} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := defaultKokoroImportEnv()
	env.lookupPython = func() (string, error) { return "/nonexistent/python3", nil }
	env.probe = func(context.Context, string) error { return nil }

	// The convert fails (no interpreter at that path), but the fetch seam is
	// the real one: reuse has to keep it off the network.
	err := ensureKokoroV1EnglishPack(t.Context(), root, env, nil)
	if err == nil || !strings.Contains(err.Error(), "kokoro v1 convert") {
		t.Fatalf("error = %v, want the convert step to be the one that failed", err)
	}
	for _, name := range []string{kokoroV1OnnxName, kokoroV1VoicesName} {
		if !completeFile(filepath.Join(stage, name)) {
			t.Errorf("%s did not survive a failed convert", name)
		}
	}
}

func TestFetchOrReuse(t *testing.T) {
	const body = "downloaded-bytes"

	tests := []struct {
		name string
		// setup prepares dest/local under dir and returns the local path.
		setup   func(t *testing.T, dir, dest string) string
		want    string
		wantHit bool
	}{
		{
			name: "a complete dest is reused without downloading",
			setup: func(t *testing.T, _, dest string) string {
				if err := os.WriteFile(dest, []byte("staged"), 0o644); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			want: "staged",
		},
		{
			name: "an empty dest is not reused",
			setup: func(t *testing.T, _, dest string) string {
				if err := os.WriteFile(dest, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			want:    body,
			wantHit: true,
		},
		{
			name: "a part file is never reused",
			setup: func(t *testing.T, dir, dest string) string {
				part := dest + ".part"
				if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
					t.Fatal(err)
				}
				return part
			},
			want:    body,
			wantHit: true,
		},
		{
			name: "a cached local copy wins over the network",
			setup: func(t *testing.T, dir, _ string) string {
				local := filepath.Join(dir, "cached.bin")
				if err := os.WriteFile(local, []byte("cached"), 0o644); err != nil {
					t.Fatal(err)
				}
				return local
			},
			want: "cached",
		},
		{
			name:    "nothing on disk downloads",
			setup:   func(*testing.T, string, string) string { return "" },
			want:    body,
			wantHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hit atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit.Store(true)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			dir := t.TempDir()
			dest := filepath.Join(dir, kokoroV1OnnxName)
			local := tt.setup(t, dir, dest)

			var gotPct float64
			err := fetchOrReuse(t.Context(), srv.URL, dest, kokoroV1OnnxName, local, func(_ string, pct float64) { gotPct = pct })
			if err != nil {
				t.Fatalf("fetchOrReuse() error = %v", err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || string(got) != tt.want {
				t.Fatalf("dest = %q err=%v, want %q", got, err, tt.want)
			}
			if hit.Load() != tt.wantHit {
				t.Errorf("server hit = %v, want %v", hit.Load(), tt.wantHit)
			}
			if gotPct <= 0 {
				t.Errorf("progress ended at %v, want a completion tick", gotPct)
			}
		})
	}
}

// probeKokoroConverter runs a real interpreter, so a stub script stands in for
// python3: the failure must name the missing module and the install command.
func TestProbeKokoroConverter(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "missing module",
			script:  "#!/bin/sh\necho \"ModuleNotFoundError: No module named 'numpy'\" >&2\nexit 1\n",
			wantErr: "No module named 'numpy'",
		},
		{
			name:   "imports succeed",
			script: "#!/bin/sh\nexit 0\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			python := filepath.Join(t.TempDir(), "python3")
			if err := os.WriteFile(python, []byte(tt.script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := probeKokoroConverter(t.Context(), python)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("probe error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("probe error = %v, want it to name %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "pip3 install numpy onnx") {
				t.Errorf("probe error = %v, want it to say how to fix it", err)
			}
		})
	}
}

// The download cache is protected for as long as the pack it installs is
// wanted, so `models clean` cannot delete an install in progress.
func TestKokoroDownloadCacheIsClaimedWithThePack(t *testing.T) {
	modelsDir := t.TempDir()
	claims := ttsConfigClaims(&Config{TTSProvider: "kokoro"}, modelsDir)
	want := filepath.Join(modelsDir, kokoroV1DownloadsSubdir)
	for _, c := range claims {
		for _, own := range c.own {
			if own == want {
				return
			}
		}
	}
	t.Fatalf("no claim owns %s; claims = %+v", want, claims)
}
