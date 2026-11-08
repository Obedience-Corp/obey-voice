package archiveutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameRejectsPortableTraversal(t *testing.T) {
	for _, name := range []string{"", "/absolute", "../escape", "pkg/../escape", "pkg/../../escape", "C:/file", `C:\file`, `\\server\share`, `dir\..\file`, "nul\x00name"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Name(name); err == nil {
				t.Fatal("accepted unsafe name")
			}
		})
	}
}

func TestStripperPreservesReleaseLayouts(t *testing.T) {
	for _, first := range []string{"./model/", "model/", "model/sub/"} {
		var s Stripper
		if _, err := s.Strip(first, true); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Strip("./model/sub/worker", false); err != nil || got != "sub/worker" {
			t.Fatalf("%s: %s, %v", first, got, err)
		}
		if _, err := s.Strip("other/file", false); err == nil {
			t.Fatal("accepted mismatched release root")
		}
	}
	var flat Stripper
	for _, name := range []string{"worker", "sub/model"} {
		if got, err := flat.Strip(name, false); err != nil || got != name {
			t.Fatalf("flat layout changed: %s, %v", got, err)
		}
	}
}

func TestCreateLinksRejectsUnsafeGraphs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []Link
	}{
		{"absolute", []Link{{"link", "/outside"}}},
		{"volume", []Link{{"link", "C:/outside"}}},
		{"escape", []Link{{"link", "../outside"}}},
		{"dangling", []Link{{"link", "missing"}}},
		{"cycle", []Link{{"a", "b"}, {"b", "a"}}},
		{"duplicate", []Link{{"a", "file"}, {"a", "file"}}},
		{"chained escape", []Link{{"sub/parent", ".."}, {"escape", "sub/parent/.."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := CreateLinks(root, tc.links); err == nil {
				t.Fatal("accepted unsafe graph")
			}
		})
	}
}

func TestCreateLinksCanonicalizesForwardAliasesForPromotion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "real"), []byte("library"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	links := []Link{{"bin/alias", "../ignored/intermediate"}, {"ignored/intermediate", "../bin/real"}}
	if err := CreateLinks(root, links, "bin"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "ignored")); err != nil {
		t.Fatal(err)
	}
	if got, err := root.ReadFile("bin/alias"); err != nil || string(got) != "library" {
		t.Fatalf("alias depends on unpromoted directory: %q, %v", got, err)
	}
}

func TestCreateLinksRejectsUnpromotedTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ignored"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := CreateLinks(root, []Link{{"bin/alias", "../ignored"}}, "bin"); err == nil {
		t.Fatal("accepted target omitted from final tree")
	}
}
