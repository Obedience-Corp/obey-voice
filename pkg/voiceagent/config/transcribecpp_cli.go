package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// TranscribeCPPCLIVersion is the pinned upstream transcribe.cpp release the
// managed CLI is built from. transcribe.cpp is pre-1.0 and 0.2.0 broke both its
// API and its on-disk ABI, so bumping this is a PR with a rebuilt, re-checksummed
// pack — never a float onto upstream's latest.
const TranscribeCPPCLIVersion = "0.2.2"

// TranscribeCPPCLIName is the CLI's filename and the transcribecpp_binary
// default: a bare command name, so an unset key means "look on PATH".
const TranscribeCPPCLIName = "transcribe-cli"

// transcribeCPPCLISubdir holds the managed CLI, under the same models subdir as
// the GGUF the provider loads with it.
const transcribeCPPCLISubdir = transcribeCPPSubdir + "/bin"

const transcribeCPPCLIReleaseBase = "https://github.com/Obedience-Corp/transcribe-cpp-builds/releases/download/transcribecpp-cli-v" + TranscribeCPPCLIVersion + "/"

// TranscribeCPPCLISource is the upstream project the managed binary is built
// from; it travels with the asset as the MIT attribution.
const TranscribeCPPCLISource = "https://github.com/handy-computer/transcribe.cpp"

// transcribeCPPCLIBuild is one platform's prebuilt CLI tarball. Upstream's own
// release tarballs carry only the shared libraries — the CLI is a cmake
// examples/ target — so these are our builds of upstream's pinned source, and
// they are tar.bz2 to match the archive format the asset installer extracts.
type transcribeCPPCLIBuild struct {
	URL    string
	SHA256 string
}

// transcribeCPPCLIBuilds is keyed by GOOS/GOARCH. Adding a platform is one row:
// build the pin, publish the tarball, paste the checksum.
var transcribeCPPCLIBuilds = map[string]transcribeCPPCLIBuild{
	"darwin/arm64": {
		URL:    transcribeCPPCLIReleaseBase + "transcribe-cli-" + TranscribeCPPCLIVersion + "-darwin-arm64.tar.bz2",
		SHA256: "13d7a7924f210f824b32bbcf003cc8530a7000e212f1eb254ff3fc0a37c04597",
	},
	// linux/amd64 has no build yet. Leaving the row out is what makes doctor and
	// the provider say "build it yourself" there instead of promising an ensure
	// that would 404.
}

// TranscribeCPPCLIAsset is the managed transcribe.cpp CLI for this machine. ok
// is false when no build is published for this platform, and callers must then
// leave the CLI out of the manifest rather than offering an unfetchable asset.
func TranscribeCPPCLIAsset() (Asset, bool) {
	return transcribeCPPCLIAssetFor(runtime.GOOS, runtime.GOARCH)
}

func transcribeCPPCLIAssetFor(goos, goarch string) (Asset, bool) {
	build, ok := transcribeCPPCLIBuilds[goos+"/"+goarch]
	if !ok {
		return Asset{}, false
	}
	return Asset{
		ID:         "stt.transcribecpp.cli." + goos + "-" + goarch,
		Provider:   STTProviderTranscribeCPP,
		Mode:       STTModeOffline,
		Kind:       AssetKindSTT,
		Name:       TranscribeCPPCLIName + " " + TranscribeCPPCLIVersion,
		TargetDir:  transcribeCPPCLISubdir,
		Archive:    &AssetArchive{URL: build.URL, SHA256: build.SHA256},
		CheckFiles: []string{TranscribeCPPCLIName},
		License:    "MIT",
		Source:     TranscribeCPPCLISource,
	}, true
}

// ManagedTranscribeCPPBinary is where the managed CLI lands under modelsDir.
// The path is the same on every platform whether or not a build is published,
// because callers decide on the file, not on the table.
func ManagedTranscribeCPPBinary(modelsDir string) string {
	return filepath.Join(modelsDir, filepath.FromSlash(transcribeCPPCLISubdir), TranscribeCPPCLIName)
}

// ResolveTranscribeCPPBinary finds the transcribe.cpp CLI in the order a user
// expects to be obeyed: an explicit transcribecpp_binary path, then PATH, then
// the copy models ensure manages. Managed is last on purpose — someone who
// installed or built their own CLI keeps running it after an ensure.
//
// lookPath is injected so callers can resolve without touching the real PATH;
// nil means exec.LookPath.
func ResolveTranscribeCPPBinary(cfg *Config, modelsDir string, lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	configured := TranscribeCPPCLIName
	if cfg != nil {
		if v := strings.TrimSpace(cfg.TranscribeCPPBinary); v != "" {
			configured = v
		}
	}

	// A configured path is a decision, not a hint: it is used or it fails, and
	// never falls through to PATH or the managed copy behind the user's back.
	if strings.ContainsRune(configured, filepath.Separator) {
		if executableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("transcribecpp_binary %q is not an executable file", configured)
	}

	if path, err := lookPath(configured); err == nil {
		return path, nil
	}

	if managed := ManagedTranscribeCPPBinary(modelsDir); executableFile(managed) {
		return managed, nil
	}

	return "", fmt.Errorf("transcribe.cpp CLI %q not found on PATH or under %s; %s",
		configured, modelsDir, TranscribeCPPCLIRemediation())
}

// TranscribeCPPCLIRemediation is the single remediation every surface prints for
// a missing CLI. It is honest per platform: only a platform we publish a build
// for is told that models ensure can fix it.
func TranscribeCPPCLIRemediation() string {
	return transcribeCPPCLIRemediationFor(runtime.GOOS, runtime.GOARCH)
}

func transcribeCPPCLIRemediationFor(goos, goarch string) string {
	if _, ok := transcribeCPPCLIAssetFor(goos, goarch); ok {
		return "run 'obey-voice models ensure' to install it, or set transcribecpp_binary to your own build"
	}
	return fmt.Sprintf("no transcribe.cpp CLI is bundled for %s/%s yet: build transcribe.cpp v%s from source and set transcribecpp_binary to its transcribe-cli",
		goos, goarch, TranscribeCPPCLIVersion)
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
