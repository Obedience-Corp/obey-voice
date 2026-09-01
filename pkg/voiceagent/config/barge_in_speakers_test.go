package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBargeInSpeakersDefaultsToAnyone(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := BargeInSpeakersOf(cfg); got != BargeInSpeakersAnyone {
		t.Errorf("BargeInSpeakersOf = %q, want %q", got, BargeInSpeakersAnyone)
	}
	if err := ValidateBargeInSpeakers(cfg); err != nil {
		t.Errorf("ValidateBargeInSpeakers(default) = %v, want nil", err)
	}
}

func TestBargeInSpeakersEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BARGE_IN_SPEAKERS", "enrolled_only")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := BargeInSpeakersOf(cfg); got != BargeInSpeakersEnrolledOnly {
		t.Errorf("BargeInSpeakersOf = %q, want %q from env", got, BargeInSpeakersEnrolledOnly)
	}
}

// TestBargeInSpeakersOfNormalizes documents the VoiceFrontendOf-style split:
// only empty falls back (to anyone); everything else — including garbage —
// passes through unchanged so ValidateBargeInSpeakers is the one place that
// rejects it.
func TestBargeInSpeakersOfNormalizes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"unset (zero value)", "", BargeInSpeakersAnyone},
		{"whitespace only", "   ", BargeInSpeakersAnyone},
		{"anyone", "anyone", BargeInSpeakersAnyone},
		{"enrolled_only", "enrolled_only", BargeInSpeakersEnrolledOnly},
		{"case-insensitive", "ENROLLED_ONLY", BargeInSpeakersEnrolledOnly},
		{"padded", "  enrolled_only  ", BargeInSpeakersEnrolledOnly},
		{"garbage passes through", "owner_only", "owner_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BargeInSpeakers: tt.value}
			if got := BargeInSpeakersOf(cfg); got != tt.want {
				t.Errorf("BargeInSpeakersOf(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
	if got := BargeInSpeakersOf(nil); got != BargeInSpeakersAnyone {
		t.Errorf("BargeInSpeakersOf(nil) = %q, want %q", got, BargeInSpeakersAnyone)
	}
}

// TestValidateBargeInSpeakersRejectsGarbage documents why BargeInSpeakersOf's
// permissive pass-through cannot be the last word: a typo must be a hard
// error, not a silent slide into the more permissive "anyone" (D7).
func TestValidateBargeInSpeakersRejectsGarbage(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"unset", "", false},
		{"anyone", "anyone", false},
		{"enrolled_only", "enrolled_only", false},
		{"case-insensitive", "Enrolled_Only", false},
		{"typo", "enrolled_onyl", true},
		{"unrelated value", "owner_only", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBargeInSpeakers(&Config{BargeInSpeakers: tt.value})
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBargeInSpeakers(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
	if err := ValidateBargeInSpeakers(nil); err != nil {
		t.Errorf("ValidateBargeInSpeakers(nil) = %v, want nil", err)
	}
}

// TestBargeInSpeakerVerdictMaxAgeOf documents the 2x-window staleness bound
// (D7) and its fallback when speaker.live.window_ms is unset.
func TestBargeInSpeakerVerdictMaxAgeOf(t *testing.T) {
	tests := []struct {
		name     string
		windowMS int
		want     time.Duration
	}{
		{"unset (zero value) uses the 1500ms speaker default", 0, 3 * time.Second},
		{"negative treated as unset", -1, 3 * time.Second},
		{"configured window scales 2x", 1000, 2 * time.Second},
		{"large window", 3000, 6 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Speaker.Live.WindowMS = tt.windowMS
			if got := BargeInSpeakerVerdictMaxAgeOf(cfg); got != tt.want {
				t.Errorf("BargeInSpeakerVerdictMaxAgeOf(windowMS=%d) = %v, want %v", tt.windowMS, got, tt.want)
			}
		})
	}
	if got := BargeInSpeakerVerdictMaxAgeOf(nil); got != 3*time.Second {
		t.Errorf("BargeInSpeakerVerdictMaxAgeOf(nil) = %v, want 3s", got)
	}
}
