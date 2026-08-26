package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTranscribeCPPModelRejectsUnknownName(t *testing.T) {
	cases := []struct {
		name       string
		configured string
	}{
		{name: "unknown slug", configured: "parakeet-tdt-0.6b-v9"},
		{name: "parakeet_model spelling is not this key", configured: "tdt-0.6b-v2"},
		{name: "path escape", configured: "../evil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := TranscribeCPPModel(tc.configured)
			if err == nil {
				t.Fatalf("TranscribeCPPModel(%q) error = nil, want unsupported model", tc.configured)
			}
			if !strings.Contains(err.Error(), tc.configured) {
				t.Errorf("error %v should name the configured model %q", err, tc.configured)
			}
		})
	}
}

func TestTranscribeCPPModelDefaults(t *testing.T) {
	for _, configured := range []string{"", DefaultTranscribeCPPModel} {
		asset, err := TranscribeCPPModel(configured)
		if err != nil {
			t.Fatalf("TranscribeCPPModel(%q) error = %v", configured, err)
		}
		if asset.Name != DefaultTranscribeCPPModel {
			t.Errorf("name = %q, want %q", asset.Name, DefaultTranscribeCPPModel)
		}
		if asset.SHA256 == "" || asset.Size == 0 {
			t.Errorf("asset %+v must be checksummed and sized (managed download)", asset)
		}
		if asset.License != "cc-by-4.0" {
			t.Errorf("license = %q, want cc-by-4.0 (Parakeet weights need attribution)", asset.License)
		}
		if want := filepath.Join("transcribecpp", asset.Filename); asset.RelPath() != want {
			t.Errorf("RelPath() = %q, want %q", asset.RelPath(), want)
		}
	}
}

func TestSTTProviderEnumOffersTranscribeCPPWithoutMovingTheDefault(t *testing.T) {
	if !slices.Contains(sttProviderEnum(), "transcribecpp") {
		t.Fatalf("stt_provider enum %v is missing transcribecpp", sttProviderEnum())
	}
	spec, ok := SpecFor("stt_provider")
	if !ok {
		t.Fatal("stt_provider key spec missing")
	}
	if spec.Default != "sherpa-parakeet" {
		t.Fatalf("stt_provider default = %v, want sherpa-parakeet (transcribecpp is optional)", spec.Default)
	}
}

func TestNormalizeTranscribeCPPIsOfflineOnly(t *testing.T) {
	norm, err := NormalizeSTTWithMode("transcribecpp", "")
	if err != nil {
		t.Fatalf("NormalizeSTTWithMode(transcribecpp) error = %v", err)
	}
	if norm.Provider != STTProviderTranscribeCPP || norm.Mode != STTModeOffline {
		t.Fatalf("norm = %+v, want transcribecpp/offline", norm)
	}
	if _, err := NormalizeSTTWithMode("transcribecpp", STTModeStreaming); err == nil {
		t.Fatal("transcribecpp + stt_mode=streaming should fail; streaming is not a v1 mode")
	}
}

func TestManifestPlansTheGGUFOnlyForTranscribeCPP(t *testing.T) {
	asset, err := TranscribeCPPModel("")
	if err != nil {
		t.Fatal(err)
	}

	selected := &Config{STTProvider: "transcribecpp"}
	m, err := ManifestFor(selected, DefaultAssetRequest(selected))
	if err != nil {
		t.Fatalf("ManifestFor(transcribecpp) error = %v", err)
	}
	files := m.ModelFiles()
	if len(files) != 1 || files[0].Name != asset.RelPath() {
		t.Fatalf("planned files = %+v, want the GGUF at %s", files, asset.RelPath())
	}
	if files[0].SHA256 != asset.SHA256 || files[0].Size != asset.Size {
		t.Errorf("planned file %+v must carry the pinned checksum and size", files[0])
	}

	for _, provider := range []string{"", "sherpa-parakeet", "sherpa", "whispercpp"} {
		cfg := &Config{STTProvider: provider, WhisperCPPModel: "base.en", WhisperModel: "small"}
		other, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
		if err != nil {
			t.Fatalf("ManifestFor(%q) error = %v", provider, err)
		}
		for _, f := range other.ModelFiles() {
			if strings.Contains(f.Name, "transcribecpp") {
				t.Errorf("stt_provider %q planned %q; the GGUF is only for transcribecpp", provider, f.Name)
			}
		}
	}
}

func TestManifestForUnknownTranscribeCPPModelErrors(t *testing.T) {
	cfg := &Config{STTProvider: "transcribecpp", TranscribeCPPModel: "bogus"}
	_, err := ManifestFor(cfg, AssetRequest{NeedSTT: true})
	if err == nil || !strings.Contains(err.Error(), "transcribe.cpp") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %v, want it to name the provider and the model", err)
	}
}

func TestTranscribeCPPAssetCarriesAttributionIntoTheManifest(t *testing.T) {
	cfg := &Config{STTProvider: "transcribecpp"}
	m, err := ManifestFor(cfg, DefaultAssetRequest(cfg))
	if err != nil {
		t.Fatalf("ManifestFor(transcribecpp) error = %v", err)
	}
	if len(m.Assets) != 1 {
		t.Fatalf("planned %d assets, want 1", len(m.Assets))
	}
	if m.Assets[0].License != "cc-by-4.0" {
		t.Errorf("planned asset license = %q, want cc-by-4.0", m.Assets[0].License)
	}
	if !strings.Contains(m.Assets[0].Source, "huggingface.co") {
		t.Errorf("planned asset source = %q, want the weights' origin", m.Assets[0].Source)
	}
	st := m.Status(t.TempDir())
	if st[0].License != m.Assets[0].License || st[0].Source != m.Assets[0].Source {
		t.Errorf("status = %+v, want it to carry the asset's license and source", st[0])
	}
}
