package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostedPersonaSharingDefaultsSetAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("brain_provider: ollama\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func() string {
		t.Helper()
		SetConfigDirForTest(t, dir)
		cfg, err := LoadWithoutHooks()
		if err != nil {
			t.Fatal(err)
		}
		return cfg.HostedPersonaSharing
	}
	if got := load(); got != "from_invite" {
		t.Fatalf("default = %q", got)
	}
	for _, value := range []string{"", "bogus", "whole"} {
		if err := ValidateAndSet("hosted_persona_sharing", value); err == nil {
			t.Fatalf("config set accepted %q", value)
		}
	}
	if err := ValidateAndSet("hosted_persona_sharing", "whole_conversation"); err != nil {
		t.Fatal(err)
	}
	if got := load(); got != "whole_conversation" {
		t.Fatalf("saved = %q", got)
	}
	for _, value := range []string{"from_invite", "bogus"} {
		t.Setenv("HOSTED_PERSONA_SHARING", value)
		if got := load(); got != value {
			t.Fatalf("env %q loaded as %q", value, got)
		}
	}
}
