package qwen

import (
	"runtime"
	"strings"
	"testing"
)

func TestDefaultNativeReleaseForDarwinArm64(t *testing.T) {
	url, sha := DefaultNativeReleaseFor("darwin", "arm64")
	if !strings.Contains(url, "qwen3-tts-native-darwin-arm64.tar.gz") {
		t.Fatalf("url = %q", url)
	}
	if !strings.HasPrefix(url, "https://github.com/Obedience-Corp/qwen3-tts-native/releases/download/") {
		t.Fatalf("url should be GitHub release asset: %q", url)
	}
	if len(sha) != 64 {
		t.Fatalf("sha length = %d, want 64 hex", len(sha))
	}
}

func TestDefaultNativeReleaseForLinuxAmd64(t *testing.T) {
	url, sha := DefaultNativeReleaseFor("linux", "amd64")
	if !strings.Contains(url, "qwen3-tts-native-linux-amd64.tar.gz") {
		t.Fatalf("url = %q", url)
	}
	if !strings.Contains(url, "/v0.1.1/") {
		t.Fatalf("linux pin should be v0.1.1 (CPU), got %q", url)
	}
	if len(sha) != 64 {
		t.Fatalf("sha length = %d, want 64 hex", len(sha))
	}
}

func TestDefaultNativeReleaseMatchesThisPlatform(t *testing.T) {
	wantURL, wantSHA := DefaultNativeReleaseFor(runtime.GOOS, runtime.GOARCH)
	gotURL, gotSHA := DefaultNativeRelease()
	if gotURL != wantURL || gotSHA != wantSHA {
		t.Fatalf("DefaultNativeRelease() = %q/%q, want %q/%q", gotURL, gotSHA, wantURL, wantSHA)
	}
}

func TestUnavailableHintNamesThisPlatform(t *testing.T) {
	got := UnavailableHint()
	want := "no published package for " + runtime.GOOS + "/" + runtime.GOARCH
	if !strings.Contains(got, want) {
		t.Fatalf("UnavailableHint() = %q, want it to name %q", got, want)
	}
	if strings.Contains(got, "models ensure --tts") {
		t.Fatalf("UnavailableHint must not advertise models ensure as a working download: %q", got)
	}
}

func TestInstallRemediationDoesNotPromiseMissingPackage(t *testing.T) {
	if ResolveNativeURL("") != "" {
		got := InstallRemediation("")
		if !strings.Contains(got, "models ensure --tts") {
			t.Fatalf("published platform remediation = %q, want models ensure", got)
		}
		return
	}
	got := InstallRemediation("")
	if !strings.Contains(got, UnavailableHint()) {
		t.Fatalf("unpublished platform remediation = %q, want UnavailableHint", got)
	}
	if strings.Contains(got, "downloads the published platform package") {
		t.Fatalf("must not promise a download that does not exist: %q", got)
	}
	custom := InstallRemediation("https://example.invalid/pkg.tar.gz")
	if !strings.Contains(custom, "models ensure --tts") || strings.Contains(custom, "no published package") {
		t.Fatalf("custom URL remediation = %q, want a download path", custom)
	}
}

func TestDefaultNativeReleaseDarwinArm64Resolve(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("platform defaults are per-OS")
	}
	url, sha := DefaultNativeRelease()
	if got := ResolveNativeURL(""); got != url {
		t.Fatalf("ResolveNativeURL() = %q, want default %q", got, url)
	}
	if gotURL, gotSHA := ResolveNativeDownload("", ""); gotURL != url || gotSHA != sha {
		t.Fatalf("ResolveNativeDownload() = %q/%q, want default pin %q/%q", gotURL, gotSHA, url, sha)
	}
	// The pin's digest must never attach to a different archive: a custom URL
	// with no explicit checksum resolves to an empty SHA (archive check
	// skipped; post-extract manifest verification still covers every file).
	if gotURL, gotSHA := ResolveNativeDownload("file:///tmp/custom.tar.gz", ""); gotURL != "file:///tmp/custom.tar.gz" || gotSHA != "" {
		t.Fatalf("ResolveNativeDownload(custom) = %q/%q, want custom URL with empty SHA", gotURL, gotSHA)
	}
	// An explicit checksum always wins.
	if _, gotSHA := ResolveNativeDownload("file:///tmp/custom.tar.gz", "abc123"); gotSHA != "abc123" {
		t.Fatalf("ResolveNativeDownload(custom, sha) sha = %q, want abc123", gotSHA)
	}
}

func TestResolveNativeURLConfigWins(t *testing.T) {
	if got := ResolveNativeURL("https://example.invalid/pkg.tar.gz"); got != "https://example.invalid/pkg.tar.gz" {
		t.Fatalf("got %q", got)
	}
}
