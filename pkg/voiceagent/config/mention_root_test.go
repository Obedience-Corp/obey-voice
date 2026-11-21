package config

import (
	"os"
	"path/filepath"
	"testing"
)

func canonicalDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestMentionRootIsTheCampaignRootOfTheDefaultDirectory(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	root := writeCampaignMarker(t, canonicalDir(t, t.TempDir()), validMarker)
	launch := filepath.Join(root, "projects", "app")
	if err := os.MkdirAll(launch, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := canonicalDir(t, t.TempDir())

	base := &Config{WorkDir: launch, WorkDirSource: "launch-dir"}
	alpha, beta := base.Clone(), base.Clone()
	if err := BindPersonaDirectory(alpha, "alpha", elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := BindPersonaDirectory(beta, "beta", ""); err != nil {
		t.Fatal(err)
	}
	if alpha.WorkDir == beta.WorkDir {
		t.Fatal("test needs personas with different directories")
	}
	for name, cfg := range map[string]*Config{"unbound": base, "alpha": alpha, "beta": beta} {
		got, campaign := MentionRoot(cfg, "")
		if canonicalDir(t, got) != root || !campaign {
			t.Fatalf("%s: MentionRoot = %q, %v; want %q, true", name, got, campaign, root)
		}
	}
}

func TestMentionRootOutsideACampaignIsTheDefaultDirectory(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	launch := canonicalDir(t, t.TempDir())
	pinned := canonicalDir(t, t.TempDir())
	cfg := &Config{WorkDir: launch, WorkDirSource: "launch-dir"}
	if err := BindPersonaDirectory(cfg, "alpha", pinned); err != nil {
		t.Fatal(err)
	}
	got, campaign := MentionRoot(cfg, "")
	if canonicalDir(t, got) != launch || campaign {
		t.Fatalf("MentionRoot = %q, %v; want %q, false", got, campaign, launch)
	}
}

func TestMentionRootFallsBackToTheGivenWorkDir(t *testing.T) {
	root := writeCampaignMarker(t, canonicalDir(t, t.TempDir()), validMarker)
	nested := filepath.Join(root, "docs")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, campaign := MentionRoot(&Config{}, nested); got != root || !campaign {
		t.Fatalf("MentionRoot = %q, %v; want %q, true", got, campaign, root)
	}
	outside := canonicalDir(t, t.TempDir())
	if got, campaign := MentionRoot(nil, outside); got != outside || campaign {
		t.Fatalf("MentionRoot = %q, %v; want %q, false", got, campaign, outside)
	}
}

func TestMentionRootIsStatedOnlyWithTheEnvironmentBlock(t *testing.T) {
	if !MentionRootStated(nil) || !MentionRootStated(&Config{EnvironmentContextEnabled: true}) {
		t.Fatal("the environment block states the mention root")
	}
	if MentionRootStated(&Config{}) {
		t.Fatal("without the environment block nothing states the mention root")
	}
}
