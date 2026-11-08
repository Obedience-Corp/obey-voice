package phonon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractRejectsChainedSourceLinkEscape(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range []tar.Header{
		{Name: "source/d/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "source/d/parent", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0777},
		{Name: "source/escape", Typeflag: tar.TypeSymlink, Linkname: "d/parent/../..", Mode: 0777},
	} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "source.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extract(archive, filepath.Join(dir, "dest")); err == nil {
		t.Fatal("accepted an escaping source link chain")
	}
}

func TestExtractPreservesInternalLicenseLink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range []tar.Header{
		{Name: "source/sub/LICENSE", Typeflag: tar.TypeSymlink, Linkname: "../LICENSE", Mode: 0777},
		{Name: "source/LICENSE", Typeflag: tar.TypeReg, Mode: 0644},
	} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "source.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")
	if err := extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(dest, "source/sub/LICENSE")); err != nil {
		t.Fatalf("internal license alias broke: %v", err)
	}
}
