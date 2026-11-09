package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureVersion = "v0.3.0-7-g1234567-dirty"

func stageFixture(t *testing.T, script string) func(string) error {
	t.Helper()
	return func(stage string) error {
		for _, name := range []string{"obey-voice", "LICENSE", "NOTICE", "lib/libsherpa-onnx-c-api.so", "lib/libonnxruntime.so", "licenses/sherpa-onnx-go-linux-LICENSE", "licenses/onnxruntime-LICENSE", "licenses/onnxruntime-ThirdPartyNotices.txt"} {
			path := filepath.Join(stage, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			contents := "fixture " + name
			if name == "obey-voice" {
				contents = "#!/bin/sh\necho obey-voice " + fixtureVersion + "\n"
				if script != "" {
					contents = script
				}
			}
			if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestInstallReplacesExecutableAndReusesEntryPoints(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "Go bin with spaces ' $")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dest, "obey-voice")
	if err := os.WriteFile(entry, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := installPayload(t.Context(), dest, "linux-amd64", fixtureVersion, stageFixture(t, "")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"obey-voice", "samantha"} {
		out, err := exec.CommandContext(t.Context(), filepath.Join(dest, name), "--version").CombinedOutput()
		if err != nil || !strings.Contains(string(out), fixtureVersion) {
			t.Fatalf("%s: %s %v", name, out, err)
		}
	}
	installed, err := filepath.EvalSymlinks(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(installed), "lib/libonnxruntime.so")); err != nil {
		t.Fatal(err)
	}
}

func TestReinstallRetiresOnlyOwnedPayload(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "bin")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "released-executable")
	if err := os.WriteFile(release, []byte("release stays unchanged"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dest, "obey-voice")
	if err := os.Symlink(release, entry); err != nil {
		t.Fatal(err)
	}
	install := func() {
		t.Helper()
		if _, err := installPayload(t.Context(), dest, "linux-amd64", fixtureVersion, stageFixture(t, "")); err != nil {
			t.Fatal(err)
		}
	}
	install()
	first, err := filepath.EvalSymlinks(entry)
	if err != nil {
		t.Fatal(err)
	}
	install()
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("old payload still exists: %v", err)
	}
	data, err := os.ReadFile(release)
	if err != nil || string(data) != "release stays unchanged" {
		t.Fatalf("release changed: %s %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(dest, ".obey-voice-dev"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("payloads: %v %v", entries, err)
	}
}

func TestFailedInstallPreservesWorkingCommand(t *testing.T) {
	for _, failure := range []string{"staging", "startup", "version", "missing-library", "symlink-library", "directory-collision", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			dest := t.TempDir()
			entry := filepath.Join(dest, "obey-voice")
			if err := os.WriteFile(entry, []byte("working install"), 0o755); err != nil {
				t.Fatal(err)
			}
			if failure == "directory-collision" {
				if err := os.Mkdir(filepath.Join(dest, "samantha"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stage := func(path string) error {
				script := ""
				switch failure {
				case "staging":
					return errors.New("native staging failed")
				case "startup":
					script = "#!/bin/sh\nexit 7\n"
				case "version":
					script = "#!/bin/sh\necho obey-voice v0.0.0\n"
				}
				if err := stageFixture(t, script)(path); err != nil {
					return err
				}
				lib := filepath.Join(path, "lib/libonnxruntime.so")
				if failure == "missing-library" || failure == "symlink-library" {
					if err := os.Remove(lib); err != nil {
						return err
					}
					if failure == "symlink-library" {
						return os.Symlink(entry, lib)
					}
				}
				if failure == "cancel" {
					cancel()
				}
				return nil
			}
			if _, err := installPayload(ctx, dest, "linux-amd64", fixtureVersion, stage); err == nil {
				t.Fatal("expected failed install")
			}
			data, err := os.ReadFile(entry)
			if err != nil || string(data) != "working install" {
				t.Fatalf("working install changed: %s %v", data, err)
			}
			entries, err := os.ReadDir(filepath.Join(dest, ".obey-voice-dev"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed staging left behind: %v", entries)
			}
		})
	}
}

func TestInstallSmokeEnvironment(t *testing.T) {
	env := cleanSmokeEnv([]string{"HOME=personal", "XDG_CONFIG_HOME=personal", "XDG_CACHE_HOME=personal", "DYLD_LIBRARY_PATH=hidden", "LD_PRELOAD=hidden", "OBEY_VOICE_CONFIG=personal", "SAMANTHA_CONFIG=personal", "PATH=/bin"}, "/scratch")
	got := strings.Join(env, "\n")
	if strings.Contains(got, "personal") || strings.Contains(got, "hidden") || !strings.Contains(got, "HOME=/scratch") || !strings.Contains(got, "PATH=/bin") {
		t.Fatal(got)
	}
}

func TestNativeInstallPlatform(t *testing.T) {
	for _, tc := range []struct{ os, arch, want string }{{"darwin", "arm64", "macos-arm64"}, {"linux", "amd64", "linux-amd64"}, {"linux", "arm64", ""}, {"windows", "amd64", ""}} {
		got, err := nativePlatform(tc.os, tc.arch)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("%+v: %s %v", tc, got, err)
		}
	}
}
