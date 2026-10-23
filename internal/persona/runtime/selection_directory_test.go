package personaruntime

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"testing"
)

func TestSelectionAndSettingsReloadKeepSessionDirectory(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	first := testProfile("samantha", "Samantha", "samantha")
	other := testProfile("jarvis", "Jarvis", "jarvis")
	other.StartingDirectory = b
	f := newSelectorTestFixture(t, first, other)
	defer f.close(t)
	f.selector.RestoreSession(f.session.ID, "samantha", "Samantha", "", config.WorkDirInfo{Dir: a, Source: "persona"})
	if _, err := f.selector.Select(t.Context(), "samantha", "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selector.Select(t.Context(), "jarvis", "ui"); err != nil {
		t.Fatal(err)
	}
	if got := f.selector.Config(); got.WorkDir != a || got.WorkDirSource != "persona" {
		t.Fatal(got.WorkDir, got.WorkDirSource)
	}
	changed := f.base.Clone()
	changed.WorkDir = b
	if err := f.selector.Reload(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if got := f.selector.Snapshot(); got.WorkDir != a || got.WorkDirSource != "persona" {
		t.Fatal(got)
	}
	other.StartingDirectory = "/missing/edited/persona/folder"
	writeTestProfile(t, f.catalog.Dir, other)
	if _, err := f.selector.Select(t.Context(), "jarvis", "response_boundary"); err != nil {
		t.Fatal(err)
	}
	if f.selector.Config().WorkDir != a {
		t.Fatal("profile edit moved existing conversation")
	}
}
