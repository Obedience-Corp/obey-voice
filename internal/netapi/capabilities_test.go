package netapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestCapabilitiesAdvertiseThisBuild pins the flags a client feature-detects
// on. Dropping one is the failure that matters: a client would silently
// conclude an up-to-date serve cannot stream text.
func TestCapabilitiesAdvertiseThisBuild(t *testing.T) {
	want := []string{
		CapResponseDeltaPayload,
		CapThinkingDeltaPayload,
		CapAudioLevelPayload,
		CapInterruptEvents,
		CapVoiceClaimOnDequeue,
		CapBinaryAudio,
	}

	caps := Capabilities()
	if len(caps) != len(want) {
		t.Fatalf("capabilities = %v, want exactly %v", caps, want)
	}
	for _, flag := range want {
		if !caps[flag] {
			t.Errorf("capability %q is not advertised", flag)
		}
	}
}

// TestProtocolVersionUnchangedForAdditiveWire holds the doc to its own rule:
// "feature-detect by status rather than by protocol_version, which does not
// change for additive routes". Everything OV0005 adds to /v1/stream is
// additive, so the version must not move — a bump would make every older
// client's version gate refuse a serve that is strictly more capable.
func TestProtocolVersionUnchangedForAdditiveWire(t *testing.T) {
	if ProtocolVersion != 3 {
		t.Fatalf("ProtocolVersion = %d, want 3: the OV0005 wire changes are additive and gate on capabilities", ProtocolVersion)
	}
}

// TestBannerFixtureCarriesCapabilities reads back the ready line a real
// `serve --banner-json` wrote. A capability that exists only in Capabilities()
// and never reaches stdout is invisible to the supervisor that reads it.
func TestBannerFixtureCarriesCapabilities(t *testing.T) {
	tests := []struct {
		fixture string
		version float64
	}{
		{"ready_banner.json", float64(ProtocolVersion)},
		{"status.json", float64(ProtocolVersion)},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "wire", tt.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}
			if doc["protocol_version"] != tt.version {
				t.Errorf("protocol_version = %v, want %v", doc["protocol_version"], tt.version)
			}
			caps, ok := doc["capabilities"].(map[string]any)
			if !ok {
				t.Fatalf("no capabilities object in %s; re-capture it", tt.fixture)
			}
			for flag := range Capabilities() {
				if caps[flag] != true {
					t.Errorf("%s does not advertise %q (got %v)", tt.fixture, flag, caps[flag])
				}
			}
			if len(caps) != len(Capabilities()) {
				t.Errorf("%s capabilities = %v, want the %d this build advertises", tt.fixture, caps, len(Capabilities()))
			}
		})
	}
}

// TestBannerFixtureHasNoLiveSecret guards the fixtures themselves: they are
// captured from a real serve, and a real bearer must never land in git. It
// walks every file under testdata/wire rather than the two banners, because the
// captures that arrived later are .jsonl — a guard that covered only the
// banners would wave through the first stream capture that carried a
// credential. An extension it does not know how to read is a failure, not a
// skip: a fixture nothing checks is exactly how one leaks.
func TestBannerFixtureHasNoLiveSecret(t *testing.T) {
	dir := filepath.Join("testdata", "wire")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			ext := filepath.Ext(name)
			if ext == ".md" {
				t.Skip("prose, not a capture")
			}
			if ext != ".json" && ext != ".jsonl" {
				t.Fatalf("fixture %s is not checked for secrets; teach this test how to read it", name)
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			// .json is one document; .jsonl is one frame per line.
			if ext == ".json" {
				checkNoSecret(t, name, data)
				return
			}
			line := 0
			for raw := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
				line++
				checkNoSecret(t, fmt.Sprintf("%s:%d", name, line), raw)
			}
		})
	}
}

// checkNoSecret fails when one captured document carries a live credential.
func checkNoSecret(t *testing.T, where string, data []byte) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", where, err)
	}
	for _, secret := range []string{"token", "fingerprint"} {
		if v, present := doc[secret]; present && v != "REDACTED" {
			t.Errorf("%s: %q = %v, want REDACTED", where, secret, v)
		}
	}
}
