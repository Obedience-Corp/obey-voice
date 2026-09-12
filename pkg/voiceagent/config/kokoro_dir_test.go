package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKokoroDirFromKeepsExplicitRuntimeAssetsIsolated(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	v1 := filepath.Join(first, KokoroV1Subdir)
	if err := os.MkdirAll(v1, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string(nil), kokoroRequired...), ".kokoro-v1-en-source") {
		if err := os.WriteFile(filepath.Join(v1, name), []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ root, want string }{{first, v1}, {second, second}, {first, v1}} {
		if got := KokoroDirFrom(&Config{ModelsDir: tc.root}); got != tc.want {
			t.Fatalf("runtime models %q selected %q, want %q", tc.root, got, tc.want)
		}
	}
}

func TestKokoroDirPrefersV1Pack(t *testing.T) {
	root := t.TempDir()
	// Point ModelsDir at temp via viper defaults is hard; set env + Load.
	t.Setenv("MODELS_DIR", root)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	if got := KokoroDir(); got != root {
		t.Fatalf("without v1 pack KokoroDir = %q, want models root %q", got, root)
	}
	if got := KokoroPack(); got != "sherpa-multi-lang-v1_0" {
		t.Fatalf("KokoroPack = %q", got)
	}

	v1 := filepath.Join(root, KokoroV1Subdir)
	for _, name := range kokoroRequired {
		p := filepath.Join(v1, name)
		if name == "espeak-ng-data" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(v1, ".kokoro-v1-en-source"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := KokoroDir(); got != v1 {
		t.Fatalf("with v1 pack KokoroDir = %q, want %q", got, v1)
	}
	if got := KokoroPack(); got != "thewh1teagle-v1.0-en" {
		t.Fatalf("KokoroPack = %q", got)
	}
}
