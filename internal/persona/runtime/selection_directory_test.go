package personaruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestSelectionAndSettingsReloadKeepEachPersonaDirectory(t *testing.T) {
	a, _ := filepath.EvalSymlinks(t.TempDir())
	b, _ := filepath.EvalSymlinks(t.TempDir())
	first := testProfile("samantha", "Samantha", "samantha")
	other := testProfile("jarvis", "Jarvis", "jarvis")
	other.StartingDirectory = b
	f := newSelectorTestFixture(t, first, other)
	defer f.close(t)
	f.selector.RestoreSession(f.session.ID, "samantha", "Samantha", "", &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: a, Source: "config"}, Personas: map[string]config.ConversationDirectory{"samantha": {Dir: a, Source: "persona"}}})
	if _, err := f.selector.Select(t.Context(), "samantha", "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "jarvis", "ui"); err != nil {
		t.Fatal(err)
	}
	if got := f.selector.Config(); got.WorkDir != b || got.WorkDirSource != "persona" {
		t.Fatal(got.WorkDir, got.WorkDirSource)
	}
	changed := f.base.Clone()
	changed.WorkDir = b
	if err := f.selector.Reload(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if got := f.selector.Snapshot(); got.WorkDir != b || got.WorkDirSource != "persona" {
		t.Fatal(got)
	}
	other.StartingDirectory = "/missing/edited/persona/folder"
	writeTestProfile(t, f.catalog.Dir, other)
	if _, err := f.selector.Select(t.Context(), "jarvis", "response_boundary"); err != nil {
		t.Fatal(err)
	}
	if f.selector.Config().WorkDir != b {
		t.Fatal("profile edit moved existing conversation")
	}
}

func TestRoutedPersonasUseOwnDirectoriesAndFailAtomically(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	launch, _ := filepath.EvalSymlinks(t.TempDir())
	jarvisDir, _ := filepath.EvalSymlinks(t.TempDir())
	otherDir, _ := filepath.EvalSymlinks(t.TempDir())
	jarvis := testProfile("jarvis", "Jarvis", "jarvis")
	jarvis.StartingDirectory = jarvisDir
	obey := testProfile("obey", "Obey", "obey")
	other := testProfile("other", "Other", "other")
	other.StartingDirectory = otherDir
	missing := testProfile("missing", "Missing", "missing")
	missing.StartingDirectory = filepath.Join(launch, "not-there")
	f := newSelectorTestFixture(t, jarvis, obey, other, missing)
	defer f.close(t)
	state := &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: launch, Source: "launch-dir"}, Personas: map[string]config.ConversationDirectory{"jarvis": {Dir: jarvisDir, Source: "persona"}}}
	f.selector.RestoreSession(f.session.ID, "jarvis", "Jarvis", "", state)
	if _, err := f.selector.Select(t.Context(), "jarvis", "resume"); err != nil {
		t.Fatal(err)
	}
	f.selector.opts.AddressResolver = addressResolverFunc(func(_ context.Context, req brain.AddressRequest) (brain.Addressee, error) {
		return brain.Addressee{Kind: "address", Name: strings.Split(req.Text, ",")[0]}, nil
	})
	for _, tc := range []struct{ name, path string }{{"Obey", launch}, {"Jarvis", jarvisDir}, {"Other", otherDir}, {"Obey", launch}} {
		if _, err := f.selector.Route(t.Context(), tc.name+", where are you working?", false); err != nil {
			t.Fatal(err)
		}
		if got := f.selector.Config().WorkDir; got != tc.path {
			t.Fatalf("%s directory = %q, want %q", tc.name, got, tc.path)
		}
		saved, err := f.store.Load(f.session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.WorkDir != tc.path || saved.Directories.Default.Dir != launch {
			t.Fatalf("persisted directory mismatch: %+v", saved)
		}
	}
	before := f.selector.Snapshot()
	beforeDisk, err := os.ReadFile(filepath.Join(filepath.Dir(f.catalog.Dir), "sessions", f.session.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Route(t.Context(), "Missing, where are you working?", false); err == nil {
		t.Fatal("missing directory admitted")
	}
	if f.selector.Snapshot() != before {
		t.Fatal("failed switch changed selection")
	}
	afterDisk, err := os.ReadFile(filepath.Join(filepath.Dir(f.catalog.Dir), "sessions", f.session.ID+".json"))
	if err != nil || string(afterDisk) != string(beforeDisk) {
		t.Fatal("failed switch changed saved session", err)
	}
	// A resumed conversation retains both personas and the launch default.
	saved, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.selector.RestoreSession(saved.ID, saved.SelectedPersonaID, saved.SelectedPersonaDisplayName, saved.SelectedPersonaPromptHash, saved.Directories)
	if _, err := f.selector.Select(t.Context(), "jarvis", "resume"); err != nil {
		t.Fatal(err)
	}
	if f.selector.Config().WorkDir != jarvisDir {
		t.Fatal("resume lost Jarvis directory")
	}
	if _, err := f.selector.Select(t.Context(), "obey", "ui"); err != nil {
		t.Fatal(err)
	}
	if f.selector.Config().WorkDir != launch {
		t.Fatal("resume lost default directory")
	}
}
