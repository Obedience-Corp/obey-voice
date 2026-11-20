//go:build !integration

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestServeManagementSettingsPersistAndKeepEnvironmentPrecedence(t *testing.T) {
	dir := personaEnv(t)
	t.Setenv("TTS_LOOKAHEAD", "4")
	manager := newServeConfigManagement()
	settings, err := manager.Settings()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"tts_worker_token", "openai_speech_api_key", "remote_tools_enabled"} {
		if strings.Contains(string(raw), `"key":"`+hidden+`"`) {
			t.Fatalf("exposed %s", hidden)
		}
		if _, err := manager.SetSetting(hidden, "true"); err == nil {
			t.Fatalf("accepted %s", hidden)
		}
	}
	if _, err := manager.SetSetting("tts_lookahead", "3"); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "tts_lookahead: 3") {
		t.Fatalf("file = %s", file)
	}
	if _, err := manager.Settings(); err != nil {
		t.Fatal(err)
	}
	value, _, _ := config.ValueFor("tts_lookahead")
	if value.Source != config.SourceEnv || value.Value != "4" {
		t.Fatalf("env value = %#v", value)
	}
	if _, err := manager.SetSetting("tts_lookahead", "nope"); err == nil {
		t.Fatal("accepted invalid integer")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(after) != string(file) {
		t.Fatal("invalid write changed file")
	}
}

func TestServeManagementPersonaPreservesWorkerAndPrompt(t *testing.T) {
	personaEnv(t)
	seedUncleFu(t)
	p, err := persona.Load("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	p.Brain.Worker = persona.Worker{Provider: "ollama", Model: "worker-model", OllamaHost: "http://worker"}
	enabled := true
	p.Brain.WorkerTiers = &config.WorkerTierOverrides{Simple: &config.WorkerTierOverride{Enabled: &enabled}}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}
	manager := newServeConfigManagement()
	if _, err := manager.EditPersona("uncle-fu", "brain_model", "new-model"); err != nil {
		t.Fatal(err)
	}
	p, err = persona.Load("uncle-fu")
	if err != nil {
		t.Fatal(err)
	}
	if p.Brain.Model != "new-model" || p.Brain.Worker.Model != "worker-model" || p.Brain.Worker.OllamaHost != "http://worker" {
		t.Fatalf("brain = %+v", p.Brain)
	}
	if p.Brain.WorkerTiers == nil || p.Brain.WorkerTiers.Simple == nil || p.Brain.WorkerTiers.Simple.Enabled == nil || !*p.Brain.WorkerTiers.Simple.Enabled {
		t.Fatalf("worker tiers changed: %+v", p.Brain.WorkerTiers)
	}
	prompt, err := persona.DescribePrompt(p)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Body != "You are Uncle Fu." {
		t.Fatalf("prompt changed: %q", prompt.Body)
	}
	if _, err := manager.EditPersona("uncle-fu", "prompt-file", "/etc/passwd"); err == nil {
		t.Fatal("accepted host file reference")
	}
	writeUserPrompt(t, "uncle-fu", structuredPromptDoc)
	if _, err := manager.EditPersona("uncle-fu", "prompt", "overwrite"); err == nil {
		t.Fatal("accepted destructive structured prompt edit")
	}
}

func TestServeToolPolicyUsesRemotePermissionForLiveAndTieredWork(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		local, remote, tiered bool
	}{
		{"local permission only", true, false, false},
		{"remote permission only", false, true, false},
		{"local tiers and no remote permission", true, false, true},
		{"permitted remote tiers", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := config.Config{VoiceToolsEnabled: tc.local, RemoteToolsEnabled: tc.remote, VoiceWorkers: config.DefaultVoiceWorkers()}
			if tc.tiered {
				local.VoiceWorkers.Mode = config.WorkerModeTiered
			}
			session := local
			applyServeToolPolicy(&session, local.RemoteToolsEnabled)
			if session.VoiceToolsEnabled != tc.remote {
				t.Fatal("live permission differs from host remote setting")
			}
			if tc.tiered && !tc.remote {
				if session.VoiceWorkers != nil || !session.VoiceWorkersDisabled {
					t.Fatal("tool-capable tiered jobs remain available")
				}
			} else if session.VoiceWorkers.Mode != local.VoiceWorkers.Mode || session.VoiceWorkersDisabled {
				t.Fatal("unexpected worker configuration change")
			}
			if local.VoiceToolsEnabled != tc.local || (local.VoiceWorkers.Mode == config.WorkerModeTiered) != tc.tiered {
				t.Fatal("local configuration was modified")
			}
		})
	}
}
