package prompts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSeedWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	created, err := Seed(dir)
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	// Every shipped document lands, named for the document rather than the
	// file it is embedded as. Companion persona prompts (samantha, veronica,
	// jarvis, santa, dylan) sit beside obey. Samantha also still has turn and compact
	// documents because a profile can reference those kinds by name.
	want := map[string]bool{
		filepath.Join("persona", "obey.yaml"):        true,
		filepath.Join("persona", "samantha.yaml"):    true,
		filepath.Join("persona", "veronica.yaml"):    true,
		filepath.Join("persona", "jarvis.yaml"):      true,
		filepath.Join("persona", "santa.yaml"):       true,
		filepath.Join("persona", "dylan.yaml"):       true,
		filepath.Join("turn", "obey.yaml"):           true,
		filepath.Join("turn", "samantha.yaml"):       true,
		filepath.Join("compact", "obey.yaml"):        true,
		filepath.Join("compact", "samantha.yaml"):    true,
		filepath.Join("postprocess", "patch.yaml"):   true,
		filepath.Join("postprocess", "rewrite.yaml"): true,
	}
	for _, rel := range created {
		delete(want, rel)
	}
	if len(want) != 0 {
		t.Errorf("Seed() missing expected files %v (created %v)", want, created)
	}

	// Seeded files must load and match the embedded documents' hashes.
	shipped, err := Shipped()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range shipped {
		path := filepath.Join(dir, string(def.Prompt.Kind), def.Prompt.Name+".yaml")
		doc, err := LoadFile(path, def.Prompt.Kind)
		if err != nil {
			t.Fatalf("loading seeded %s: %v", path, err)
		}
		if doc.Hash() != def.Hash() {
			t.Errorf("seeded %s/%s hash %s != embedded %s", def.Prompt.Kind, def.Prompt.Name, doc.Hash(), def.Hash())
		}
	}

	// The manifest beside the documents is not a prompt: every entry still
	// reads as the embedded default.
	entries, err := Catalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Source != SourceEmbedded {
			t.Errorf("Catalog() after Seed lists %s/%s as %s (%s)", e.Kind, e.Name, e.Source, e.Path)
		}
	}
}

func TestSeedNeverOverwritesEdits(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "persona", "samantha.yaml")
	custom := []byte("schema: obey-voice.prompt.v1\nprompt:\n  name: samantha\n  kind: persona\n  system_prompt: Custom.\n")
	writeFile(t, path, custom)

	written, err := Seed(dir)
	if err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	if len(written) != 0 {
		t.Errorf("second Seed() wrote %v, want nothing", written)
	}
	assertFile(t, path, custom)
}

// Two releases in a row: the copy the first wrote follows the second, while an
// edited copy and the current copy are left exactly as they are.
func TestSeedRefreshesCopiesFromAnEarlierRelease(t *testing.T) {
	dir := t.TempDir()
	v1 := []seedSource{
		{rel: "system/a.yaml", data: []byte("a: 1\n")},
		{rel: "system/b.yaml", data: []byte("b: 1\n")},
		{rel: "system/c.yaml", data: []byte("c: 1\n")},
	}
	if _, err := seed(dir, v1, nil); err != nil {
		t.Fatal(err)
	}
	edited := []byte("b: 1\n# my note\n")
	writeFile(t, filepath.Join(dir, "system", "b.yaml"), edited)

	v2 := []seedSource{
		{rel: "system/a.yaml", data: []byte("a: 2\n")},
		{rel: "system/b.yaml", data: []byte("b: 2\n")},
		{rel: "system/c.yaml", data: []byte("c: 1\n")},
	}
	written, err := seed(dir, v2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(written, []string{filepath.Join("system", "a.yaml")}) {
		t.Errorf("seed() wrote %v, want only system/a.yaml", written)
	}
	assertFile(t, filepath.Join(dir, "system", "a.yaml"), v2[0].data)
	assertFile(t, filepath.Join(dir, "system", "b.yaml"), edited)
	assertFile(t, filepath.Join(dir, "system", "c.yaml"), v1[2].data)

	// The refreshed copy is again the product's, so a third release moves it on.
	v3 := []seedSource{{rel: "system/a.yaml", data: []byte("a: 3\n")}}
	if _, err := seed(dir, v3, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, "system", "a.yaml"), v3[0].data)
}

// An install seeded before the manifest existed holds byte-exact copies of
// earlier releases; the real ones recorded in testdata must be refreshed.
func TestSeedRefreshesReleasedCopiesWithoutManifest(t *testing.T) {
	for name, rel := range map[string]string{
		"conversation-history.old.yaml": "system/conversation-history.yaml",
		"worker-image-repair.old.yaml":  "system/worker-image-repair.yaml",
	} {
		old, err := os.ReadFile(filepath.Join("testdata", "seed", name))
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		writeFile(t, dst, old)

		written, err := Seed(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(written, filepath.FromSlash(rel)) {
			t.Errorf("Seed() did not refresh %s (wrote %v)", rel, written)
		}
		assertFile(t, dst, shippedBytes(t, rel))
	}
}

func TestSeedKeepsEditsWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	old, err := os.ReadFile(filepath.Join("testdata", "seed", "conversation-history.old.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	edited := append(old, []byte("# keep this\n")...)
	dst := filepath.Join(dir, "system", "conversation-history.yaml")
	writeFile(t, dst, edited)
	own := []byte("schema: obey-voice.prompt.v1\nprompt:\n  name: mine\n  kind: persona\n  system_prompt: Mine.\n")
	ownPath := filepath.Join(dir, "persona", "mine.yaml")
	writeFile(t, ownPath, own)

	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dst, edited)
	assertFile(t, ownPath, own)
}

func TestSeedSurvivesCorruptManifest(t *testing.T) {
	dir := t.TempDir()
	old, err := os.ReadFile(filepath.Join("testdata", "seed", "worker-image-repair.old.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, seedManifestName), []byte("{not json"))
	stale := filepath.Join(dir, "system", "worker-image-repair.yaml")
	writeFile(t, stale, old)
	editedPath := filepath.Join(dir, "turn", "obey.yaml")
	edited := []byte("schema: obey-voice.prompt.v1\nprompt:\n  name: obey\n  kind: turn\n  system_prompt: Short.\n")
	writeFile(t, editedPath, edited)

	if _, err := Seed(dir); err != nil {
		t.Fatalf("Seed() with a corrupt manifest error = %v", err)
	}
	assertFile(t, stale, shippedBytes(t, "system/worker-image-repair.yaml"))
	assertFile(t, editedPath, edited)

	written, err := Seed(dir)
	if err != nil || len(written) != 0 {
		t.Errorf("Seed() after repairing the manifest = %v, %v; want no writes", written, err)
	}
}

func TestSeedUnwritableManifestStillSeeds(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, seedManifestName), 0o755); err != nil {
		t.Fatal(err)
	}
	editedPath := filepath.Join(dir, "turn", "obey.yaml")
	edited := []byte("schema: obey-voice.prompt.v1\nprompt:\n  name: obey\n  kind: turn\n  system_prompt: Short.\n")
	writeFile(t, editedPath, edited)

	if _, err := Seed(dir); err == nil {
		t.Error("Seed() with an unwritable manifest returned no error")
	}
	assertFile(t, filepath.Join(dir, "persona", "obey.yaml"), shippedBytes(t, "persona/obey.yaml"))
	assertFile(t, editedPath, edited)
}

func TestSeedSecondRunIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, seedManifestName)
	before, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("Seed() left no manifest: %v", err)
	}

	written, err := Seed(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("second Seed() wrote %v", written)
	}
	after, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("second Seed() rewrote the manifest")
	}
}

func TestSeedLeavesSymlinksAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.yaml")
	old, err := os.ReadFile(filepath.Join("testdata", "seed", "conversation-history.old.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, old)
	link := filepath.Join(dir, "system", "conversation-history.yaml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Seed() replaced the symlink: %v, %v", info, err)
	}
	assertFile(t, target, old)
}

func shippedBytes(t *testing.T, rel string) []byte {
	t.Helper()
	sources, err := seedSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range sources {
		if src.rel == rel {
			return src.data
		}
	}
	t.Fatalf("no shipped document at %s", rel)
	return nil
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s =\n%s\nwant\n%s", path, got, want)
	}
}
