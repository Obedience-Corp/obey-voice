package decider

import (
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestCatalogPinsAndPreservesLegacySelections(t *testing.T) {
	legacy := map[string]string{"2b": "533964dae8be954c5b5e19fa4948e48408094c1e", "0.8b": "a0a01d6f8135298f400a8c856b355793012ae971"}
	for _, id := range ModelIDs() {
		model, err := ModelFor(id)
		if err != nil {
			t.Fatal(err)
		}
		if model.Name == "" || model.License == "" || model.MemoryGiB <= 0 || model.DownloadBytes() == 0 {
			t.Fatalf("incomplete catalog %s", id)
		}
		if want := legacy[id]; want != "" && model.Revision != want {
			t.Fatalf("legacy selection %s moved", id)
		}
		if len(model.Revision) != 40 {
			t.Fatalf("unpinned revision %s", id)
		}
		for _, f := range model.Files {
			hash, err := hex.DecodeString(f.SHA256)
			if err != nil || len(hash) != 32 || f.Size <= 0 || filepath.IsAbs(f.Name) || filepath.Clean(f.Name) != f.Name {
				t.Fatalf("invalid pinned file %s/%s", id, f.Name)
			}
		}
	}
	if model, _ := ModelFor(""); model.Revision != legacy[DefaultModel] {
		t.Fatal("default changed")
	}
	if _, err := ModelFor("made-up-model"); err == nil {
		t.Fatal("unknown model accepted")
	}
}

func TestDeviceMatrix(t *testing.T) {
	for _, tc := range []struct {
		os, arch, request string
		gpu               bool
		want              string
	}{
		{"darwin", "arm64", "auto", false, "mps"}, {"darwin", "arm64", "cpu", false, "cpu"},
		{"linux", "amd64", "auto", true, "cuda"}, {"linux", "amd64", "auto", false, "cpu"},
		{"linux", "amd64", "cpu", true, "cpu"}, {"linux", "arm64", "auto", true, "cpu"},
		{"linux", "arm64", "cuda", true, ""}, {"linux", "amd64", "mps", false, ""},
		{"darwin", "arm64", "cuda", true, ""}, {"darwin", "amd64", "cpu", false, ""},
		{"windows", "amd64", "auto", false, ""}, {"linux", "amd64", "typo", true, ""},
	} {
		t.Run(tc.os+tc.arch+tc.request+tc.want, func(t *testing.T) {
			got, err := DeviceFor(tc.os, tc.arch, tc.request, tc.gpu)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v want %q", got, err, tc.want)
			}
		})
	}
}

func TestNativeRuntimeProfiles(t *testing.T) {
	if RuntimeDir("models", "cpu") == RuntimeDir("models", "mps") {
		t.Fatal("device profiles collide")
	}
	for _, platform := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		a, err := NativeAsset(platform[0], platform[1], "cpu")
		if err != nil || len(a.SHA256) != 64 || a.Size <= 0 {
			t.Fatalf("unverified runtime: %+v %v", a, err)
		}
	}
}
