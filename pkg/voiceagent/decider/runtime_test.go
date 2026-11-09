//go:build darwin || linux

package decider

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeNative(t *testing.T, root, script string) {
	t.Helper()
	device := defaultDevice()
	exe := NativePath(root, device)
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	hash, err := fileHash(t.Context(), exe)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := NativeAsset(runtime.GOOS, runtime.GOARCH, device)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(installedNative{Revision: RuntimeRevision, Archive: asset.SHA256, Files: map[string]string{"llama-server": hash}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), "installed.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestVerifiedNativeRuntimeDoesNotRequireArchive(t *testing.T) {
	root := t.TempDir()
	device := defaultDevice()
	fakeNative(t, root, "#!/bin/sh\nexit 0\n")
	if !NativeReadyFor(root, "2b", device) {
		t.Fatal("native runtime not ready")
	}
	if err := EnsureNativeFor(t.Context(), root, "2b", device, func(string) { t.Error("verified runtime reinstalled") }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NativePath(root, device), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if NativeReadyFor(root, "2b", device) {
		t.Fatal("tampered worker accepted")
	}
	if err := EnsureNativeFor(t.Context(), root, "2b", device, nil); err == nil {
		t.Fatal("missing archive silently repaired runtime")
	}
}
func TestNativeInstallRejectsCorruptArchive(t *testing.T) {
	root := t.TempDir()
	device := defaultDevice()
	a, err := NativeAsset(runtime.GOOS, runtime.GOARCH, device)
	if err != nil {
		t.Fatal(err)
	}
	archive := RuntimeArchivePath(root, a)
	if err = os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archive, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	err = EnsureNativeFor(t.Context(), root, "2b", device, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error=%v", err)
	}
	if NativeReadyFor(root, "2b", device) {
		t.Fatal("failed install marked ready")
	}
}
func TestLegacyRuntimeNeverCountsAsNative(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "decider", "runtime-old", "venv", "bin")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(legacy, "python")
	if err := os.WriteFile(exe, []byte("legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	if NativeReadyFor(root, "2b", defaultDevice()) {
		t.Fatal("legacy Python counted as native")
	}
	if _, err := Start(t.Context(), root, "2b"); err == nil {
		t.Fatal("started legacy runtime")
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "legacy" {
		t.Fatal("legacy assets changed")
	}
}

func TestInstallLockCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	release, err := installLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, err = installLock(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("competing lock=%v", err)
	}
	release()
	release, err = installLock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestLocalProcessEnvironmentExcludesCredentialsAndOverrides(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "DECISION_API_KEY", "HF_TOKEN", "PYTHONPATH", "UV_INDEX_URL", "HTTPS_PROXY", "DECIDER_MODEL"} {
		t.Setenv(key, "must-not-inherit")
	}
	for _, value := range cleanEnvironment() {
		if strings.Contains(value, "must-not-inherit") {
			t.Fatalf("leaked process override: %s", strings.SplitN(value, "=", 2)[0])
		}
	}
}

func TestServerStartupFailureAndCancellationCleanUp(t *testing.T) {
	for _, script := range []string{"#!/bin/sh\nexit 7\n", "#!/bin/sh\nexec sleep 30\n"} {
		t.Run(strings.TrimSpace(script), func(t *testing.T) {
			root := t.TempDir()
			fakeNative(t, root, script)
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			model, _ := ModelFor("2b")
			server, err := startNative(ctx, root, "unused-fixture.gguf", model, defaultDevice())
			if server != nil || err == nil {
				if server != nil {
					server.Close()
				}
				t.Fatalf("server=%v err=%v", server, err)
			}
			if strings.Contains(err.Error(), "no such file") {
				t.Fatalf("relative model directory failed: %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("startup failed to join cancelled child")
			}
		})
	}
}

func TestNativeArchiveMaterializesOnlySafeLibraryLinks(t *testing.T) {
	for _, target := range []string{"libreal.dylib", "../../outside", "/outside", "libalias.dylib"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "runtime.tar.gz")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			for _, h := range []*tar.Header{{Name: "release/libreal.dylib", Mode: 0600, Size: 3, Typeflag: tar.TypeReg}, {Name: "release/libalias.dylib", Typeflag: tar.TypeSymlink, Linkname: target}} {
				if err = tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Typeflag == tar.TypeReg {
					_, _ = tw.Write([]byte("lib"))
				}
			}
			tw.Close()
			gz.Close()
			f.Close()
			dest := filepath.Join(root, "output")
			if err := os.Mkdir(dest, 0700); err != nil {
				t.Fatal(err)
			}
			err = extractNative(t.Context(), archive, dest)
			if target != "libreal.dylib" {
				if err == nil {
					t.Fatal("unsafe link accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(dest, "libalias.dylib"))
			if err != nil || !info.Mode().IsRegular() {
				t.Fatal("library link was not materialized")
			}
			got, err := os.ReadFile(filepath.Join(dest, "libalias.dylib"))
			if err != nil || string(got) != "lib" {
				t.Fatal("wrong library content")
			}
		})
	}
}

func TestStartRequiresExplicitModelRepair(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("weights-present=%t", present), func(t *testing.T) {
			root := t.TempDir()
			fakeNative(t, root, "#!/bin/sh\nexit 1\n")
			model, err := ModelFor("2b")
			if err != nil {
				t.Fatal(err)
			}
			dir, err := ModelDir(root, "2b")
			if err != nil {
				t.Fatal(err)
			}
			weights := filepath.Join(dir, model.Files[0].Name)
			if present {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(weights, []byte("corrupt fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := StartFor(t.Context(), root, "2b", defaultDevice()); err == nil || !strings.Contains(err.Error(), "Settings > Brain") || !strings.Contains(err.Error(), "models decision install") {
				t.Fatalf("setup error=%v", err)
			}
			if present {
				raw, err := os.ReadFile(weights)
				if err != nil || string(raw) != "corrupt fixture" {
					t.Fatalf("startup repaired model: %q %v", raw, err)
				}
			} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("startup created model directory: %v", err)
			}
		})
	}
}
