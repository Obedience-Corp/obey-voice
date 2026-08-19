package config

import "testing"

// Asset manifests written before the obey-voice rename declare the samantha
// schema tag; installed model directories must keep validating.
func TestAssetManifestAcceptsLegacySchema(t *testing.T) {
	m := sampleManifest()
	m.Schema = legacyAssetSchema
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate(legacy schema) error = %v", err)
	}
	m.Schema = "samantha.assets.v2"
	if err := m.Validate(); err == nil {
		t.Fatal("Validate(unknown schema) error = nil, want rejection")
	}
}
