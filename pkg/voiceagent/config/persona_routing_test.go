package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersonaRoutingTimeoutBounds(t *testing.T) {
	for _, tc := range []struct{ seconds, want int }{
		{0, 120}, {-1, 120}, {1, 1}, {45, 45}, {300, 300}, {1000000, 300},
	} {
		if got := PersonaRoutingTimeoutOf(&Config{PersonaRoutingTimeout: tc.seconds}); got != time.Duration(tc.want)*time.Second {
			t.Errorf("timeout(%d)=%v, want %ds", tc.seconds, got, tc.want)
		}
	}
	if got := PersonaRoutingTimeoutOf(nil); got != 120*time.Second {
		t.Fatalf("nil config=%v", got)
	}
}

func TestPersonaRoutingTimeoutPersistsAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("brain_provider: ollama\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PersonaRoutingTimeout != 120 {
		t.Fatalf("default=%d", cfg.PersonaRoutingTimeout)
	}
	if err := ValidateAndSet("persona_routing_timeout", "45"); err != nil {
		t.Fatal(err)
	}
	// A fresh loader proves persistence rather than observing Viper's override.
	SetConfigDirForTest(t, dir)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PersonaRoutingTimeout != 45 {
		t.Fatalf("saved=%d", cfg.PersonaRoutingTimeout)
	}
	for _, value := range []string{"0", "-1", "301", "forever"} {
		if err := ValidateAndSet("persona_routing_timeout", value); err == nil {
			t.Fatalf("accepted invalid timeout %s", value)
		}
	}
	t.Setenv("PERSONA_ROUTING_TIMEOUT", "90")
	SetConfigDirForTest(t, dir)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PersonaRoutingTimeout != 90 {
		t.Fatalf("env override=%d", cfg.PersonaRoutingTimeout)
	}
}
