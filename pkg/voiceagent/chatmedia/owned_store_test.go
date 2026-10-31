package chatmedia

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedMediaRejectsDerivedAndLaterSymlinks(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "introduced later"}[late], func(t *testing.T) {
			selected := Store{Dir: t.TempDir()}
			outside := t.TempDir()
			if err := os.Mkdir(filepath.Join(selected.Dir, "media"), 0700); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(selected.Dir, "media", "session-a")
			var owned Store
			var err error
			if late {
				owned, err = selected.OwnedSubdir("media", "session-a")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, child); err != nil {
				t.Fatal(err)
			}
			if !late {
				if _, err := selected.OwnedSubdir("media", "session-a"); !errors.Is(err, ErrOwnedMediaPath) {
					t.Fatal(err)
				}
				return
			}
			session := NewAudioSession()
			defer session.Close()
			ref, err := session.ImportSamples(t.Context(), []float32{.1})
			if err != nil {
				t.Fatal(err)
			}
			data, err := session.ResolveAudio(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := owned.PersistAudio(t.Context(), ref, data); err == nil {
				t.Fatal("derived symlink escaped selected store")
			}
			if err := owned.RemoveOwned(); !errors.Is(err, ErrOwnedMediaPath) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("outside directory was written or removed", err)
			}
		})
	}
}

func TestOwnedRemovalPreservesOtherNamespacesAndDoesNotFollowNestedLinks(t *testing.T) {
	selected := Store{Dir: t.TempDir()}
	owned, err := selected.OwnedSubdir("media", "session-a")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "private.wav")
	if err := os.WriteFile(marker, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(owned.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(owned.Dir, "nested-link")); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(selected.Dir, "media", "session-b")
	if err := os.Mkdir(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := owned.RemoveOwned(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned.Dir); !os.IsNotExist(err) {
		t.Fatal("owned directory remains", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unrelated" {
		t.Fatal("nested link target changed", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal("other session removed", err)
	}
	if err := selected.RemoveOwned(); !errors.Is(err, ErrOwnedMediaPath) {
		t.Fatal("direct selected root deletion accepted", err)
	}
}
