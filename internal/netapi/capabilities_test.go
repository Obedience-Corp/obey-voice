package netapi

import (
	"encoding/json"
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
		CapAudioLevelPayload,
		CapInterruptEvents,
		CapVoiceClaimOnDequeue,
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

// TestBannerFixtureHasNoLiveSecret guards the fixture itself: these files are
// captured from a real serve, and a real bearer token must never land in git.
func TestBannerFixtureHasNoLiveSecret(t *testing.T) {
	for _, name := range []string{"ready_banner.json", "status.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", "wire", name))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"token", "fingerprint"} {
			v, present := doc[secret]
			if present && v != "REDACTED" {
				t.Errorf("%s: %q = %v, want REDACTED", name, secret, v)
			}
		}
	}
}
