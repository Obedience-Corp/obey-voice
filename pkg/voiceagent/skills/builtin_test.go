package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinSkillDiscoveryOverrideAndDisable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	loader := Loader{Dir: root, Builtins: true}
	got, err := loader.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Name != "obey-voice" || got[0].Root != BuiltinRoot || !strings.Contains(got[0].Body, "persona create") {
		t.Fatalf("built-in=%+v err=%v", got, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("embedded discovery wrote a filesystem root")
	}
	loader.Disabled = []string{" OBEY-VOICE "}
	catalog, err := loader.Catalog(t.Context())
	if err != nil || len(catalog) != 0 {
		t.Fatalf("disabled catalog=%+v err=%v", catalog, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "obey-voice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "obey-voice", "SKILL.md"), []byte("---\nname: obey-voice\ndescription: Custom app instructions\n---\nUse my local setup.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = loader.Discover(t.Context())
	if err != nil || len(got) != 1 || got[0].Root != root || !strings.Contains(got[0].Body, "my local setup") || len(got[0].Shadowed) != 1 || got[0].Shadowed[0] != BuiltinRoot {
		t.Fatalf("override=%+v err=%v", got, err)
	}
}
