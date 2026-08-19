package config

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParakeetModelResolvesNamesAndDefault(t *testing.T) {
	def, err := ParakeetModel("")
	if err != nil || def.Name != DefaultParakeetModel {
		t.Fatalf("ParakeetModel(\"\") = %+v, %v; want default %q", def, err, DefaultParakeetModel)
	}
	for _, name := range ParakeetModelNames() {
		m, err := ParakeetModel(strings.ToUpper(name))
		if err != nil {
			t.Fatalf("ParakeetModel(%q) error = %v", name, err)
		}
		if m.URL == "" || len(m.ArchiveSHA256) != 64 || m.DirName == "" {
			t.Fatalf("ParakeetModel(%q) incomplete: %+v", name, m)
		}
		if got := m.RequiredFiles(); len(got) != 4 || got[3] != "tokens.txt" {
			t.Fatalf("RequiredFiles(%q) = %v", name, got)
		}
	}
	if _, err := ParakeetModel("tdt-9b"); err == nil {
		t.Fatal("ParakeetModel(unknown) error = nil, want rejection naming the choices")
	}
}

func TestParakeetSTTAssetIsAnArchiveInItsOwnDir(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa-parakeet", ParakeetModel: "tdt-110m"}
	norm, err := NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := sttAsset(cfg, norm)
	if err != nil || asset == nil {
		t.Fatalf("sttAsset = %v, %v", asset, err)
	}
	if err := asset.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if asset.ID != "stt.parakeet.offline.tdt-110m" || asset.TargetDir == "" || asset.Archive == nil {
		t.Fatalf("asset = %+v", asset)
	}
}

// The configured parakeet_model stays protected from `models clean` even when
// another STT provider is active.
func TestParakeetModelIsClaimedWhileAnotherProviderIsActive(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa", ParakeetModel: "tdt-0.6b-v2"}
	claims, err := sttConfigClaims(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		if strings.Contains(c.reason, "parakeet_model") {
			return
		}
	}
	t.Fatalf("no claim names parakeet_model: %+v", claims)
}

// Some sherpa-onnx release archives (the Parakeet 110m model) carry "./dir/file"
// entries; the top-level directory must still be stripped.
func TestExtractTarStreamStripsDotSlashPrefixedTopLevelDir(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "./model/", typeflag: tar.TypeDir},
		{name: "./model/tokens.txt", typeflag: tar.TypeReg, body: "tok"},
		{name: "./model/encoder.int8.onnx", typeflag: tar.TypeReg, body: "enc"},
	}
	if err := extract(t, entries, dir, "test", []string{"tokens.txt", "encoder.int8.onnx"}); err != nil {
		t.Fatalf("extract error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "tokens.txt")); string(got) != "tok" {
		t.Errorf("tokens.txt = %q, want tok", got)
	}
}

func TestDoctorWarnsWhenParakeetIsEnglishOnlyForAnotherLanguage(t *testing.T) {
	cfg := &Config{STTProvider: "sherpa-parakeet", ParakeetModel: "tdt-0.6b-v2", Language: "de-DE"}
	diags := Diagnose(cfg, t.TempDir(), func(string) (string, error) { return "", os.ErrNotExist })
	for _, d := range diags {
		if d.Name == "stt-language" {
			if d.Severity != SeverityWarn || !strings.Contains(d.Remediation, "tdt-0.6b-v3") {
				t.Fatalf("stt-language = %+v", d)
			}
			return
		}
	}
	t.Fatal("no stt-language diagnostic for de-DE + English-only Parakeet")
}
