package qwen

import (
	"fmt"
	"runtime"
	"strings"
)

// nativeReleaseHost is the GitHub releases prefix. Pins are per GOOS/GOARCH and
// may live on different tags (darwin Metal is v0.1.0; linux CPU is v0.1.1).
const nativeReleaseHost = "https://github.com/Obedience-Corp/qwen3-tts-native/releases/download/"

// DefaultNativeRelease returns the platform tarball URL and archive SHA-256 for
// the pinned public release. Empty strings mean this OS/arch has no published
// asset yet (ensure will fail closed with a clear remediation).
//
// Stable asset names (no git short hash):
//
//	qwen3-tts-native-<goos>-<goarch>.tar.gz
func DefaultNativeRelease() (url, sha256 string) {
	backend := "cpu"
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		backend = DetectLinuxTTSBackend()
	}
	return DefaultNativeReleaseForBackend(runtime.GOOS, runtime.GOARCH, backend)
}

// DefaultNativeReleaseFor is the testable form without a live machine probe.
// linux/amd64 here is always the CPU pin; use DefaultNativeReleaseForBackend
// for vulkan.
func DefaultNativeReleaseFor(goos, goarch string) (url, sha256 string) {
	return DefaultNativeReleaseForBackend(goos, goarch, "cpu")
}

// DefaultNativeReleaseForBackend pins a published tarball for os/arch/backend.
func DefaultNativeReleaseForBackend(goos, goarch, backend string) (url, sha256 string) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		// qwen3-tts-native v0.1.0 — Metal 0.6B F16 package
		return nativeReleaseHost + "v0.1.0/qwen3-tts-native-darwin-arm64.tar.gz",
			"67b6e7514168d146cf4d892d6594559796bcea4f130c9c837beb8eac1c416e2f"
	case "linux/amd64":
		if strings.EqualFold(backend, "vulkan") {
			if linuxAmd64VulkanSHA256 == "" {
				return "", ""
			}
			return nativeReleaseHost + "v0.1.2/qwen3-tts-native-linux-amd64-vulkan.tar.gz",
				linuxAmd64VulkanSHA256
		}
		// qwen3-tts-native v0.1.1 — CPU 0.6B F16 (no NVIDIA required)
		return nativeReleaseHost + "v0.1.1/qwen3-tts-native-linux-amd64.tar.gz",
			"9655d41ec044a7be43404053746c6cd0d68fb11fb0e22b3a3723aa0265c1a1fd"
	default:
		return "", ""
	}
}

// linuxAmd64VulkanSHA256 is the published linux-amd64-vulkan archive digest.
// Empty until the release asset exists; ensure then fails closed.
const linuxAmd64VulkanSHA256 = "532b72718d71dc81bc25d10badce84b8231ebef68b2753ae3b577c591c0efbf8"

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
