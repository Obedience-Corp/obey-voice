package qwen

import (
	"fmt"
	"runtime"
	"strings"
)

// nativeReleaseTag is the published qwen3-tts-native GitHub release tag that
// Obey Voice uses when qwen_tts_native_url is empty. Bump when shipping a new pin.
const nativeReleaseTag = "v0.1.0"

const nativeReleaseBase = "https://github.com/Obedience-Corp/qwen3-tts-native/releases/download/" + nativeReleaseTag

// DefaultNativeRelease returns the platform tarball URL and archive SHA-256 for
// the pinned public release. Empty strings mean this OS/arch has no published
// asset yet (ensure will fail closed with a clear remediation).
//
// Stable asset names (no git short hash):
//
//	qwen3-tts-native-<goos>-<goarch>.tar.gz
func DefaultNativeRelease() (url, sha256 string) {
	return DefaultNativeReleaseFor(runtime.GOOS, runtime.GOARCH)
}

// DefaultNativeReleaseFor is the testable form of DefaultNativeRelease.
func DefaultNativeReleaseFor(goos, goarch string) (url, sha256 string) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		// qwen3-tts-native v0.1.0 — Metal 0.6B F16 package
		return nativeReleaseBase + "/qwen3-tts-native-darwin-arm64.tar.gz",
			"67b6e7514168d146cf4d892d6594559796bcea4f130c9c837beb8eac1c416e2f"
	// linux/amd64: pin qwen3-tts-native-linux-amd64.tar.gz here when a public
	// release ships. Until then status/doctor/ensure fail closed instead of
	// promising `models ensure --tts` can download a package that does not exist.
	default:
		return "", ""
	}
}

// UnavailableHint is the fail-closed line when this OS/arch has no published
// tarball. Status, doctor, and ensure must quote it instead of advertising
// `models ensure --tts` as a working download.
func UnavailableHint() string {
	return fmt.Sprintf("no published package for %s/%s — set qwen_tts_native_url to a release tarball", runtime.GOOS, runtime.GOARCH)
}

// InstallRemediation tells the user how to get the native package. When this
// platform has no pin and no URL override, it does not claim a download exists.
func InstallRemediation(configuredURL string) string {
	if strings.TrimSpace(ResolveNativeURL(configuredURL)) == "" {
		return UnavailableHint() + ", then run models ensure --tts"
	}
	return "run 'obey-voice models ensure --tts' (downloads the published platform package; override with qwen_tts_native_url if needed)"
}
