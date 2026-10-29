package config

import (
	"path/filepath"
	"testing"
)

func TestObeyEnvironmentAndWorkerModel(t *testing.T) {
	original := configFile
	configFile = filepath.Join(t.TempDir(), "config.yaml")
	defer func() { configFile = original }()
	setDefaults(v)
	for k, value := range map[string]string{"BRAIN_PROVIDER": "obey", "OBEY_PROVIDER": "ollama", "OBEY_MODEL": "voice-model", "OBEY_CAMP": "test-camp", "OBEY_SOCKET": "/tmp/test.sock", "OBEY_TOOL_POLICY": "daemon"} {
		t.Setenv(k, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrainProvider != "obey" || cfg.ObeyProvider != "ollama" || cfg.ObeyModel != "voice-model" || cfg.ObeyCamp != "test-camp" || cfg.ObeySocket != "/tmp/test.sock" || cfg.ObeyToolPolicy != "daemon" {
		t.Fatal("Obey env overrides missing")
	}
	cfg.VoiceWorkerProvider = "obey"
	cfg.VoiceWorkerModel = "worker-model"
	worker := WorkerBrainConfig(cfg)
	if BrainModelFor(worker, "obey") != "worker-model" || cfg.ObeyModel != "voice-model" || worker.ObeyCamp != "test-camp" {
		t.Fatal("worker routing incorrect")
	}
}
