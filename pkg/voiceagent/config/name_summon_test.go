package config

import "testing"

func TestNameSummonSelfTriggerRisk(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{name: "nil config", cfg: nil},
		{name: "off is never a risk", cfg: &Config{VoiceFrontend: VoiceFrontendOff}},
		{name: "on without a front end", cfg: &Config{NameSummonEnabled: true, VoiceFrontend: VoiceFrontendOff}, want: true},
		{name: "an unset front end is off", cfg: &Config{NameSummonEnabled: true}, want: true},
		{name: "on with nlms", cfg: &Config{NameSummonEnabled: true, VoiceFrontend: VoiceFrontendNLMS}},
		{name: "on with webrtc", cfg: &Config{NameSummonEnabled: true, VoiceFrontend: VoiceFrontendWebRTC}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NameSummonSelfTriggerRisk(tt.cfg); got != tt.want {
				t.Errorf("NameSummonSelfTriggerRisk() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Unlike barge-in's echo warning, this one is an error: the failure mode is the
// agent answering its own playback, so doctor must not merely mention it.
func TestDiagnoseErrorsOnNameSummonWithoutFrontend(t *testing.T) {
	cfg := healthyBrain(&Config{STTProvider: "sherpa", TTSProvider: "kokoro", NameSummonEnabled: true, VoiceFrontend: VoiceFrontendOff})
	diags := diagByName(Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", nil }))
	d, ok := diags["name-summon-echo"]
	if !ok {
		t.Fatal("expected a name-summon-echo diagnostic when name summon is on without a voice frontend")
	}
	if d.Severity != SeverityError {
		t.Errorf("name-summon-echo severity = %q, want error", d.Severity)
	}
	if d.Remediation == "" {
		t.Error("name-summon-echo has no remediation")
	}

	cfg.VoiceFrontend = VoiceFrontendNLMS
	if _, ok := diagByName(Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", nil }))["name-summon-echo"]; ok {
		t.Fatal("no name-summon-echo expected once the front end is on")
	}

	cfg.VoiceFrontend = VoiceFrontendOff
	cfg.NameSummonEnabled = false
	if _, ok := diagByName(Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", nil }))["name-summon-echo"]; ok {
		t.Fatal("no name-summon-echo expected while name summon is off")
	}
}
