package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkingDirectoryDiscoveryExcludesGlobalSkills(t *testing.T) {
	home := t.TempDir()
	restore := SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)
	global := filepath.Join(home, ".agents", "skills")
	writeSkill(t, filepath.Join(global, "turnstile-spin"), "turnstile-spin", "global website setup", "must not be injected")
	work := filepath.Join(home, "projects", "empty")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(home, ".obey", "skills")
	for _, dir := range []string{work, home, ""} {
		roots := DefaultSearchPaths(dir, app)
		for _, r := range roots {
			if sameDirectory(r, global) {
				t.Fatalf("%q imported global root: %v", dir, roots)
			}
		}
		got, err := (Loader{Dirs: roots}).Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("%q imported global skill: %+v", dir, got)
		}
	}
	// Explicit configuration is an intentional opt-in; no global files are deleted.
	roots := DefaultSearchPaths(work, global)
	got, err := (Loader{Dirs: roots}).Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "turnstile-spin" {
		t.Fatalf("explicit directory was ignored: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(global, "turnstile-spin", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingDirectoryDiscoveryPreservesProjectAndCampaign(t *testing.T) {
	home := t.TempDir()
	restore := SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)
	global := filepath.Join(home, ".agents", "skills")
	campaign := filepath.Join(home, "campaign")
	work := filepath.Join(campaign, "projects", "app")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(global, "global-only"), "global-only", "unrelated", "global body")
	writeSkill(t, filepath.Join(campaign, ".agents", "skills", "campaign-context"), "campaign-context", "workspace", "campaign body")
	writeSkill(t, filepath.Join(work, ".agents", "skills", "local"), "local", "project", "project body")
	app := filepath.Join(home, "configured-skills")
	writeSkill(t, filepath.Join(app, "local"), "local", "configured shadow", "app body")
	got, err := (Loader{Dirs: DefaultSearchPaths(work, app)}).Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range got {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"campaign-context", "local"}) {
		t.Fatalf("catalog=%v", names)
	}
	if strings.TrimSpace(got[1].Body) != "project body" {
		t.Fatalf("project precedence lost: %+v", got[1])
	}
}

func TestWorkingDirectoryDiscoveryRejectsGlobalRootAliases(t *testing.T) {
	home := t.TempDir()
	restore := SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(restore)
	global := filepath.Join(home, ".agents", "skills")
	writeSkill(t, filepath.Join(global, "global-only"), "global-only", "unrelated", "global body")
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(project, ".agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(global, filepath.Join(project, ".agents", "skills")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	nested := filepath.Join(project, "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	for _, work := range []string{project, nested, alias, filepath.Join(alias, "project", "nested")} {
		got, err := (Loader{Dirs: DefaultSearchPaths(work, "")}).Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 0 {
			t.Fatalf("%q imported global skills through alias: %+v", work, got)
		}
	}
}
