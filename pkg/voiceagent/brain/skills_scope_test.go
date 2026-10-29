package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func TestSkillsCatalogDoesNotAdvertiseSharedGlobalSkills(t *testing.T) {
	home := t.TempDir()
	restore := skills.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)
	global := filepath.Join(home, ".agents", "skills")
	writeTestSkill(t, filepath.Join(global, "turnstile-spin"), "turnstile-spin", "GLOBAL_SKILL_LEAK")
	work := filepath.Join(home, "workspace", "app")
	writeTestSkill(t, filepath.Join(work, ".agents", "skills", "project-guide"), "project-guide", "local project guidance")
	cfg := &config.Config{SkillsEnabled: true, SkillsDir: filepath.Join(home, "app-skills")}
	for _, dir := range []string{work, home} {
		catalog, err := SkillsCatalogFor(t.Context(), cfg, dir)
		if err != nil {
			t.Fatal(err)
		}
		foundLocal := false
		for _, s := range catalog {
			if s.Name == "turnstile-spin" {
				t.Fatalf("global skill entered runtime catalog from %q", dir)
			}
			foundLocal = foundLocal || s.Name == "project-guide"
		}
		if foundLocal != (dir == work) {
			t.Fatalf("working-directory scope incorrect for %q: %+v", dir, catalog)
		}
		prompt := assembleSystemPrompt("You are a helpful assistant.", dir, catalog, "", cfg)
		if strings.Contains(prompt, "GLOBAL_SKILL_LEAK") {
			t.Fatal("global skill metadata consumed prompt tokens")
		}
	}
	// Keeping a shared directory is safe: only explicitly selecting it imports it.
	cfg.SkillsDir = global
	catalog, err := SkillsCatalogFor(t.Context(), cfg, work)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range catalog {
		found = found || s.Name == "turnstile-spin"
	}
	if !found {
		t.Fatal("explicit skill directory did not opt in")
	}
	if _, err := os.Stat(filepath.Join(global, "turnstile-spin", "SKILL.md")); err != nil {
		t.Fatal("discovery changed installed skill", err)
	}
}
