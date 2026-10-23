package persona

import (
	"path/filepath"
	"testing"
)

func TestUpdateStartingDirectory(t *testing.T) {
	setConfigDir(t, t.TempDir())
	writeProfile(t, "ada", "Ada", "ada")
	dir, _ := filepath.EvalSymlinks(t.TempDir())

	p, err := UpdateStartingDirectory("ada", dir)
	if err != nil || p.StartingDirectory != dir {
		t.Fatal(p, err)
	}
	if got, _ := Load("ada"); got.StartingDirectory != dir {
		t.Fatalf("not persisted: %+v", got)
	}

	// A rejected path writes nothing: the saved folder survives.
	if _, err := UpdateStartingDirectory("ada", filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing folder accepted")
	}
	if got, _ := Load("ada"); got.StartingDirectory != dir {
		t.Fatalf("rejected edit changed profile: %+v", got)
	}
	if _, err := UpdateStartingDirectory("ada", filepath.Join(dir, "file")); err == nil {
		t.Fatal("expected a file to be rejected")
	}

	// Empty clears back to the app default.
	if _, err := UpdateStartingDirectory("ada", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load("ada"); got.StartingDirectory != "" {
		t.Fatalf("clear did not persist: %+v", got)
	}
	if _, err := UpdateStartingDirectory("Not Valid", dir); err == nil {
		t.Fatal("bad id accepted")
	}
}
