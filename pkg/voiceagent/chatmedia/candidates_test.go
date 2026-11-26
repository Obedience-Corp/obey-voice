package chatmedia

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not decoded"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Pictures in the work directory and its immediate subfolders are listed by
// name, top level first; hidden entries, deeper folders, other files and
// links that leave the directory are not.
func TestPictureNamesListsTheTopLevelAndOneFolderDown(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for _, name := range []string{
		"zebra.png", "Anchor.GIF", "notes.txt", ".hidden.png",
		"images/lion.jpg", "images/dolphin.jpeg", "images/.cache.png", "images/deep/whale.png",
		"art/sketch.gif", ".private/secret.png",
	} {
		touch(t, filepath.Join(root, name))
	}
	touch(t, filepath.Join(outside, "outside.png"))
	touch(t, filepath.Join(outside, "folder", "away.png"))
	for link, target := range map[string]string{
		"escape.png":        filepath.Join(outside, "outside.png"),
		"images/escape.png": filepath.Join(outside, "outside.png"),
		"linked":            filepath.Join(outside, "folder"),
		"inside.png":        filepath.Join(root, "zebra.png"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Anchor.GIF", "inside.png", "zebra.png", "art/sketch.gif", "images/dolphin.jpeg", "images/lion.jpg"}
	if got := PictureNames(t.Context(), root); !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
}

func TestPictureNamesIsBounded(t *testing.T) {
	root := t.TempDir()
	for i := range MaxPictureNames + 6 {
		touch(t, filepath.Join(root, "images", fmt.Sprintf("decoy-%03d.png", i)))
	}
	touch(t, filepath.Join(root, "top.png"))
	got := PictureNames(t.Context(), root)
	if len(got) != MaxPictureNames || got[0] != "top.png" || got[1] != "images/decoy-000.png" || got[len(got)-1] != fmt.Sprintf("images/decoy-%03d.png", MaxPictureNames-2) {
		t.Fatalf("%d names: first %q, last %q", len(got), got[:2], got[len(got)-1])
	}
}
