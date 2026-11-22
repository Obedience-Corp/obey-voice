package chatmedia

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
)

// A file opened for the model must be inside the work directory. The store
// holds every conversation's pictures, so unlike a reply image it is not a
// place to open from.
func TestImportWorkFileReadsOnlyTheWorkDirectory(t *testing.T) {
	work, elsewhere := t.TempDir(), t.TempDir()
	store := Store{Dir: filepath.Join(t.TempDir(), "assets")}
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{work, elsewhere, store.Dir} {
		if err := os.WriteFile(filepath.Join(dir, "shot.png"), testmedia.PNG(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(elsewhere, "shot.png"), filepath.Join(work, "link.png")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"shot.png", filepath.Join(work, "shot.png"), "file://" + filepath.Join(work, "shot.png")} {
		if _, err := store.ImportWorkFile(path, work); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(store.Dir, "shot.png"), filepath.Join(elsewhere, "shot.png"), "link.png", "../" + filepath.Base(elsewhere) + "/shot.png", "https://example.com/shot.png"} {
		if _, err := store.ImportWorkFile(path, work); err == nil {
			t.Errorf("%s was opened", path)
		}
	}
	if _, refs, err := store.ImportReply("![Shot](<"+filepath.Join(store.Dir, "shot.png")+">)", work); err != nil || len(refs) != 1 {
		t.Fatalf("a reply image in the store: %+v %v", refs, err)
	}
}
