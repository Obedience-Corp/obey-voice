package qwen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func securityArchive(t *testing.T, entries []tar.Header) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i := range entries {
		if err := tw.WriteHeader(&entries[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(name, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestExtractNativeRejectsChainedSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "installed")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dest, "install.json")
	if err := os.WriteFile(sentinel, []byte("previous install"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := securityArchive(t, []tar.Header{
		{Name: "pkg/bin/d/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "pkg/bin/d/parent", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0777},
		// Lexical cleaning stays in bin; actual resolution escapes the package.
		{Name: "pkg/bin/escape", Typeflag: tar.TypeSymlink, Linkname: "d/parent/../..", Mode: 0777},
	})
	if err := extractNativeTarGz(archive, dest); err == nil {
		t.Fatal("accepted a symlink chain escaping the package")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "previous install" {
		t.Fatalf("rejected archive changed installation: %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "bin")); !os.IsNotExist(err) {
		t.Fatalf("promoted rejected bin: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(parent, ".qwen-native-extract-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v, %v", leftovers, err)
	}
}

func TestExtractNativePreservesForwardLibraryAliases(t *testing.T) {
	archive := securityArchive(t, []tar.Header{
		{Name: "pkg/bin/libalias.so", Typeflag: tar.TypeSymlink, Linkname: "libversion.so", Mode: 0777},
		{Name: "pkg/bin/libversion.so", Typeflag: tar.TypeSymlink, Linkname: "libreal.so", Mode: 0777},
		{Name: "pkg/bin/libreal.so", Typeflag: tar.TypeReg, Mode: 0755},
	})
	dest := t.TempDir()
	if err := extractNativeTarGz(archive, dest); err != nil {
		t.Fatal(err)
	}
	// Exercise the promotion fallback against the validated tree, too.
	copy := filepath.Join(t.TempDir(), "copy")
	if err := copyPath(dest, copy); err != nil {
		t.Fatal(err)
	}
	dest = copy
	for _, name := range []string{"libalias.so", "libversion.so"} {
		resolved, err := filepath.EvalSymlinks(filepath.Join(dest, "bin", name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(filepath.Join(dest, "bin", "libreal.so"))
		if err != nil || resolved != want {
			t.Fatalf("alias %s resolves to %s, want %s: %v", name, resolved, want, err)
		}
	}
}
