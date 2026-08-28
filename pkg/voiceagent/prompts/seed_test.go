package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	created, err := Seed(dir)
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}

	// Every shipped document lands, named for the document rather than the
	// file it is embedded as. The samantha documents are in the list because
	// they still ship: the rebrand moved them out of the default slot, not out
	// of the product. All three kinds keep a samantha-named document because a
	// profile can reference any of them by name (prompts.persona, prompts.turn,
	// compact_prompt), and a fresh install must be able to resolve those refs.
	want := map[string]bool{
		filepath.Join("persona", "obey.yaml"):     true,
		filepath.Join("persona", "samantha.yaml"): true,
		filepath.Join("turn", "obey.yaml"):        true,
		filepath.Join("turn", "samantha.yaml"):    true,
		filepath.Join("compact", "obey.yaml"):     true,
		filepath.Join("compact", "samantha.yaml"): true,
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
}

func TestSeedNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "persona", "samantha.yaml")
	custom := []byte("schema: obey-voice.prompt.v1\nprompt:\n  name: samantha\n  kind: persona\n  system_prompt: Custom.\n")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := Seed(dir)
	if err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second Seed() created %v, want nothing (idempotent)", created)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Error("Seed() overwrote a user-edited file")
	}
}
