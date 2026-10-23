package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestPersonaStartingDirectoryCLI(t *testing.T) {
	personaEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	out, err := runPersona(t, "edit", "samantha", "--starting-directory", dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodePersonaResult(t, out); got.Persona.StartingDirectory != dir {
		t.Fatal(out)
	}
	p, err := persona.Load("samantha")
	if err != nil || p.StartingDirectory != dir {
		t.Fatal(p, err)
	}
	out, err = runPersona(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID                string `json:"id"`
		StartingDirectory string `json:"starting_directory"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(out, err)
	}
	found := false
	for _, row := range rows {
		if row.ID == "samantha" && row.StartingDirectory == dir {
			found = true
		}
	}
	if !found {
		t.Fatal(out)
	}
	out, err = runPersona(t, "edit", "samantha", "--display-name", "Should not save", "--starting-directory", filepath.Join(dir, "missing"), "--json")
	if err == nil {
		t.Fatal("bad directory accepted")
	}
	code, _, _ := decodeErrorJSON(t, out)
	if code != "invalid_directory" {
		t.Fatal(out)
	}
	p, _ = persona.Load("samantha")
	if p.DisplayName != "Samantha" || p.StartingDirectory != dir {
		t.Fatal("invalid edit changed profile")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := runPersona(t, "show", "samantha", "--json"); err != nil {
		t.Fatal("unavailable directory prevents repair", err)
	}
	if _, err := runPersona(t, "edit", "samantha", "--starting-directory", "", "--json"); err != nil {
		t.Fatal(err)
	}
	p, _ = persona.Load("samantha")
	if p.StartingDirectory != "" {
		t.Fatal("clear did not persist")
	}
	dir = t.TempDir()
	out, err = runPersona(t, "create", "Directory Test", "--starting-directory", dir, "--no-activate", "--json")
	if err != nil {
		t.Fatal(out, err)
	}
	var created personaCreateJSON
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.Persona.StartingDirectory == "" {
		t.Fatal(out, err)
	}
}

func TestPersonaDirectoryDiscovery(t *testing.T) {
	root := t.TempDir()
	look := func(string) (string, error) { return "camp", nil }
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`Registry updated\n[{"name":"local","path":` + quote(root) + `},{"name":"remote","path":""},{"name":"missing","path":"/no-such-camp"}]`), nil
	}
	got := discoverPersonaDirectories(t.Context(), run, look)
	if len(got.Campaigns) != 1 || got.Campaigns[0].Name != "local" || got.Notice != "" {
		t.Fatalf("%+v", got)
	}
	absent := func(string) (string, error) { return "", os.ErrNotExist }
	if got := discoverPersonaDirectories(t.Context(), run, absent); len(got.Campaigns) != 0 || got.Notice != "" {
		t.Fatal(got)
	}
	for _, run := range []func(context.Context, string, ...string) ([]byte, error){
		func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("failed") },
		func(context.Context, string, ...string) ([]byte, error) { return []byte("[{broken]"), nil },
	} {
		if got := discoverPersonaDirectories(t.Context(), run, look); got.Notice == "" || len(got.Campaigns) != 0 {
			t.Fatal(got)
		}
	}
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
