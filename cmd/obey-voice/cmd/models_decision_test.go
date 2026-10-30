package cmd

import (
	"bytes"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionSetupCommandInspectsWithoutEnablingOrInstalling(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	root := filepath.Join(t.TempDir(), "models")
	for k, v := range map[string]any{"models_dir": root, "decision_enabled": false, "decision_provider": "openai", "decision_local_model": "d1-3b"} {
		if err := config.SetAndSave(k, v); err != nil {
			t.Fatal(err)
		}
	}
	c := newDecisionSetupCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"status"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Liquid") || !strings.Contains(out.String(), root) || !strings.Contains(out.String(), "Needs install or repair") {
		t.Fatal(out.String())
	}
	cfg, err := config.LoadWithoutHooks()
	if err != nil || cfg.DecisionEnabled || cfg.DecisionProvider != "openai" {
		t.Fatal("status changed saved settings")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("status created model directory")
	}
}
