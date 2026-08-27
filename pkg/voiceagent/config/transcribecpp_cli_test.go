package config

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeManagedTranscribeCLI plants an executable stub where models ensure
// installs the CLI under modelsDir.
func writeManagedTranscribeCLI(t *testing.T, modelsDir string) string {
	t.Helper()
	path := ManagedTranscribeCPPBinary(modelsDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTranscribeCPPCLIAssetIsPublishedPerPlatform(t *testing.T) {
	for _, tc := range []struct {
		name         string
		goos, goarch string
		wantOK       bool
	}{
		{name: "linux amd64 has no build yet", goos: "linux", goarch: "amd64"},
		{name: "windows amd64 has no build yet", goos: "windows", goarch: "amd64"},
		{name: "darwin amd64 has no build yet", goos: "darwin", goarch: "amd64"},
		{name: "darwin arm64 is published", goos: "darwin", goarch: "arm64", wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asset, ok := transcribeCPPCLIAssetFor(tc.goos, tc.goarch)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if err := asset.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if asset.Archive == nil || len(asset.Archive.SHA256) != 64 {
				t.Fatalf("asset must pin a checksum: %+v", asset.Archive)
			}
			if !strings.Contains(asset.Archive.URL, TranscribeCPPCLIVersion) {
				t.Fatalf("url %q does not carry the pin %q", asset.Archive.URL, TranscribeCPPCLIVersion)
			}
			if asset.License != "MIT" || asset.Source == "" {
				t.Fatalf("MIT attribution missing: license=%q source=%q", asset.License, asset.Source)
			}
			if got := asset.CheckFiles; len(got) != 1 || got[0] != TranscribeCPPCLIName {
				t.Fatalf("check files = %v", got)
			}
		})
	}
}

// The CLI is vendored only for the provider that runs it, and only where a
// build exists — every other config must plan the same assets as before.
func TestManifestIncludesTheCLIOnlyForTranscribeCPP(t *testing.T) {
	_, published := TranscribeCPPCLIAsset()

	for _, tc := range []struct {
		name     string
		cfg      *Config
		wantCLI  bool
		wantErr  bool
		wantGGUF bool
	}{
		{
			name:    "unknown model errors before planning anything",
			cfg:     &Config{STTProvider: "transcribecpp", TranscribeCPPModel: "nope"},
			wantErr: true,
		},
		{
			name: "sherpa-parakeet plans no CLI",
			cfg:  &Config{STTProvider: "sherpa-parakeet"},
		},
		{
			name: "whispercpp plans no CLI",
			cfg:  &Config{STTProvider: "whispercpp", WhisperCPPModel: "base.en"},
		},
		{
			name:     "transcribecpp plans the GGUF and, where built, the CLI",
			cfg:      &Config{STTProvider: "transcribecpp"},
			wantGGUF: true, wantCLI: published,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ManifestFor(tc.cfg, AssetRequest{NeedSTT: true})
			if tc.wantErr {
				if err == nil {
					t.Fatal("err = nil, want a rejection naming the model")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var gotCLI, gotGGUF bool
			for _, a := range m.Assets {
				switch {
				case strings.HasPrefix(a.ID, "stt.transcribecpp.cli."):
					gotCLI = true
					if a.License != "MIT" {
						t.Fatalf("CLI asset lost its attribution: %+v", a)
					}
				case strings.HasPrefix(a.ID, "stt.transcribecpp."):
					gotGGUF = true
				}
			}
			if gotCLI != tc.wantCLI || gotGGUF != tc.wantGGUF {
				t.Fatalf("cli=%v gguf=%v; want cli=%v gguf=%v", gotCLI, gotGGUF, tc.wantCLI, tc.wantGGUF)
			}
		})
	}
}

func TestResolveTranscribeCPPBinaryOrder(t *testing.T) {
	managedOnly := t.TempDir()
	managed := writeManagedTranscribeCLI(t, managedOnly)

	explicitDir := t.TempDir()
	explicit := filepath.Join(explicitDir, "my-transcribe")
	if err := os.WriteFile(explicit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExecutable := filepath.Join(explicitDir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		cfg       Config
		modelsDir string
		installed []string
		want      string
		wantErr   string
	}{
		{
			name:    "nothing anywhere names the pin and the remedy",
			wantErr: "not found on PATH",
		},
		{
			name:    "a configured path that is not executable fails outright",
			cfg:     Config{TranscribeCPPBinary: notExecutable},
			wantErr: "is not an executable file",
		},
		{
			// A path the user set is never silently replaced by a working
			// managed copy: that would hide their typo.
			name:      "a configured path never falls back to managed",
			cfg:       Config{TranscribeCPPBinary: filepath.Join(explicitDir, "missing")},
			modelsDir: managedOnly, installed: []string{TranscribeCPPCLIName},
			wantErr: "is not an executable file",
		},
		{
			name:      "explicit beats PATH and managed",
			cfg:       Config{TranscribeCPPBinary: explicit},
			modelsDir: managedOnly, installed: []string{TranscribeCPPCLIName},
			want: explicit,
		},
		{
			name:      "PATH beats managed",
			modelsDir: managedOnly, installed: []string{TranscribeCPPCLIName},
			want: "/usr/local/bin/" + TranscribeCPPCLIName,
		},
		{
			name:      "managed is used when PATH has nothing",
			modelsDir: managedOnly,
			want:      managed,
		},
		{
			name:      "an unset key still means the default command name",
			installed: []string{TranscribeCPPCLIName},
			want:      "/usr/local/bin/" + TranscribeCPPCLIName,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.modelsDir
			if dir == "" {
				dir = t.TempDir()
			}
			got, err := ResolveTranscribeCPPBinary(&tc.cfg, dir, lookPathFor(tc.installed...))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("binary = %q, want %q", got, tc.want)
			}
		})
	}
}

// A directory at the managed path is not a binary, and a nil resolver must not
// panic the picker.
func TestResolveTranscribeCPPBinaryRejectsNonFiles(t *testing.T) {
	modelsDir := t.TempDir()
	if err := os.MkdirAll(ManagedTranscribeCPPBinary(modelsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTranscribeCPPBinary(nil, modelsDir, lookPathFor()); err == nil {
		t.Fatal("err = nil, want a rejection for a directory at the managed path")
	}
}

func TestTranscribeCPPCLIRemediationIsHonestPerPlatform(t *testing.T) {
	if got := transcribeCPPCLIRemediationFor("darwin", "arm64"); !strings.Contains(got, "models ensure") {
		t.Fatalf("published platform must offer ensure: %q", got)
	}
	got := transcribeCPPCLIRemediationFor("linux", "amd64")
	if strings.Contains(got, "models ensure") {
		t.Fatalf("unbuilt platform must not promise ensure: %q", got)
	}
	for _, want := range []string{"linux/amd64", "from source", "transcribecpp_binary", TranscribeCPPCLIVersion} {
		if !strings.Contains(got, want) {
			t.Fatalf("remediation %q is missing %q", got, want)
		}
	}
}

// The vendored executable survives a models clean whatever provider is active.
func TestManagedCLIIsClaimedAgainstClean(t *testing.T) {
	if _, ok := TranscribeCPPCLIAsset(); !ok {
		t.Skip("no CLI build published for " + runtime.GOOS + "/" + runtime.GOARCH)
	}
	modelsDir := t.TempDir()
	claims, err := sttConfigClaims(&Config{STTProvider: "sherpa-parakeet"}, modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	want := ManagedTranscribeCPPBinary(modelsDir)
	for _, c := range claims {
		for _, owned := range c.own {
			if owned == want || strings.HasPrefix(want, owned+string(filepath.Separator)) {
				return
			}
		}
	}
	t.Fatalf("no claim covers %s: %+v", want, claims)
}

// The published pack must extract to an executable file at the managed path —
// the tar's mode bits are what make the installed CLI runnable.
func TestManagedCLIExtractsExecutable(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("#!/bin/sh\nexit 0\n")
	for _, f := range []struct {
		name string
		mode int64
		body []byte
	}{
		{TranscribeCPPCLIName, 0o755, body},
		{"LICENSE", 0o644, []byte("MIT")},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	modelsDir := t.TempDir()
	target := filepath.Dir(ManagedTranscribeCPPBinary(modelsDir))
	if _, err := extractTarStream(&buf, target, "transcribe-cli", []string{TranscribeCPPCLIName}); err != nil {
		t.Fatal(err)
	}

	installed := ManagedTranscribeCPPBinary(modelsDir)
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("mode = %o, want 755", perm)
	}
	if !executableFile(installed) {
		t.Fatal("installed CLI is not executable")
	}
	// The attribution files ride along next to it.
	if _, err := os.Stat(filepath.Join(target, "LICENSE")); err != nil {
		t.Fatalf("LICENSE not installed: %v", err)
	}
	// And the resolver finds exactly this copy when PATH has nothing.
	got, err := ResolveTranscribeCPPBinary(&Config{}, modelsDir, func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err != nil || got != installed {
		t.Fatalf("resolve = %q, %v; want %q", got, err, installed)
	}
}
