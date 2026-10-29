package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func TestSkillsCLIListsOnlyWorkingDirectoryScope(t *testing.T) {
	home := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)
	config.SetConfigDirForTest(t, filepath.Join(home, "config"))
	global := filepath.Join(home, ".agents", "skills")
	writeSkillFixture(t, filepath.Join(global, "turnstile-spin"), "turnstile-spin", "unrelated global setup")
	work := filepath.Join(home, "projects", "app")
	writeSkillFixture(t, filepath.Join(work, ".agents", "skills", "project-guide"), "project-guide", "working directory")
	cfg := &config.Config{SkillsEnabled: true, BrainProvider: "ollama", WorkDir: work, SkillsDir: filepath.Join(home, "app-skills")}
	for _, dir := range []string{work, home} {
		cmd := newSkillsCmd(skillsTestConfig(cfg))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"list", "--json", "--work-dir", dir})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var env skillsListEnvelope
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		local := false
		for _, row := range env.Skills {
			if row.Name == "turnstile-spin" {
				t.Fatalf("CLI advertised global skill from %q", dir)
			}
			local = local || row.Name == "project-guide"
		}
		if local != (dir == work) {
			t.Fatalf("CLI ignored working directory: %+v", env.Skills)
		}
		for _, r := range env.Roots {
			if r.Path == global {
				t.Fatalf("CLI still advertised shared global root: %+v", env.Roots)
			}
		}
	}
	cmd := newSkillsCmd(skillsTestConfig(cfg))
	cmd.SetArgs([]string{"disable", "turnstile-spin", "--json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no skill named") {
		t.Fatalf("global skill remained discoverable through management command: %v", err)
	}
	if _, err := os.Stat(config.ConfigFile()); !os.IsNotExist(err) {
		t.Fatalf("refused global toggle wrote config: %v", err)
	}
}
