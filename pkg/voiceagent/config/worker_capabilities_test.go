package config

import (
	"encoding/json"
	"testing"
)

func TestWorkerCapabilitiesRoundTrip(t *testing.T) {
	in := WorkerCapabilities{Provider: "grok", Adapter: "grok-go-sdk", Version: "v0.2.1", Usage: Capability{Evidence: "unknown"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out WorkerCapabilities
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != in.Provider || out.Usage.Evidence != "unknown" || out.Usage.Supported {
		t.Fatalf("round trip = %+v", out)
	}
}
