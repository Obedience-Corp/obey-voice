package config

import "testing"

func TestAcousticNameModelOnlyRequestedWhenEnabledForSTT(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, need := range []bool{false, true} {
			cfg := &Config{STTProvider: "parakeet", ParakeetModel: "tdt-0.6b-v2", AcousticNameEnabled: enabled}
			m, err := ManifestFor(cfg, AssetRequest{NeedSTT: need})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, a := range m.Assets {
				if a.ID == AcousticNameAsset().ID {
					found = true
					if err := a.Validate(); err != nil {
						t.Fatal(err)
					}
					if a.Archive.SHA256 == "" || a.License == "" {
						t.Fatal("missing integrity or attribution")
					}
				}
			}
			if found != (enabled && need) {
				t.Fatalf("enabled=%v need=%v found=%v", enabled, need, found)
			}
		}
	}
}
func TestAcousticNameRejectsUnsupportedProvider(t *testing.T) {
	if _, err := ManifestFor(&Config{STTProvider: "sherpa", AcousticNameEnabled: true}, AssetRequest{NeedSTT: true}); err == nil {
		t.Fatal("silently enabled unsupported provider")
	}
}
