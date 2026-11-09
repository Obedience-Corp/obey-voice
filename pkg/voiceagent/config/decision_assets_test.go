package config

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionManifestOnlyForEnabledManagedLocal(t *testing.T) {
	base := Config{DecisionEnabled: true, DecisionVoiceWork: true, DecisionProvider: "local", DecisionRuntime: "managed", DecisionLocalModel: "2b"}
	for _, variant := range []string{"on", "off", "points-off", "external", "hosted"} {
		t.Run(variant, func(t *testing.T) {
			cfg := base
			switch variant {
			case "off":
				cfg.DecisionEnabled = false
			case "points-off":
				cfg.DecisionVoiceWork = false
			case "external":
				cfg.DecisionRuntime = "external"
			case "hosted":
				cfg.DecisionProvider = "openai"
			}
			m, err := ManifestFor(&cfg, AssetRequest{NeedDecision: true})
			if err != nil {
				t.Fatal(err)
			}
			if variant != "on" {
				if len(m.Assets) != 0 {
					t.Fatal("inactive managed mode requested assets")
				}
				return
			}
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			states := m.Status(t.TempDir())
			found := false
			for _, state := range states {
				if state.ID == "decision.runtime.environment" {
					found = true
					if state.Installed || len(state.Missing) == 0 {
						t.Fatalf("missing environment=%+v", state)
					}
				}
			}
			if !found {
				t.Fatal("status omitted private environment")
			}
		})
	}
}

func TestGzipArchiveVerificationAndPathRejection(t *testing.T) {
	for _, variant := range []string{"valid", "checksum", "escape"} {
		t.Run(variant, func(t *testing.T) {
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			archive := tar.NewWriter(gz)
			name, contents := "release/uv", "installer fixture"
			if variant == "escape" {
				name = "release/../../outside"
			}
			if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0700, Size: int64(len(contents))}); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.Write([]byte(contents)); err != nil {
				t.Fatal(err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(compressed.Bytes()))
			if variant == "checksum" {
				sum = "deadbeef"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(compressed.Bytes()) }))
			defer srv.Close()
			root := t.TempDir()
			target := filepath.Join(root, "installed")
			url := srv.URL + "/installer.tar.gz"
			err := downloadAndExtractArchive(t.Context(), target, "decision.uv.test", url, "uv", []string{"uv"}, sum, nil)
			if variant != "valid" {
				if err == nil {
					t.Fatal("invalid archive accepted")
				}
				if _, err := os.Stat(filepath.Join(target, "uv")); !os.IsNotExist(err) {
					t.Fatal("failed archive installed executable")
				}
				if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
					t.Fatal("archive escaped target")
				}
				noLeftoverTemp(t, target)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(target, "uv"))
			if err != nil || string(got) != contents {
				t.Fatalf("installed=%q err=%v", got, err)
			}
			if !archiveInstallMarkerValid(target, "decision.uv.test", url, sum, []string{"uv"}) {
				t.Fatal("verified gzip missing install marker")
			}
		})
	}
}

func TestDecisionCatalogDownloadsOnlySelectedWeightsAndRuntime(t *testing.T) {
	for _, id := range decider.ModelIDs() {
		cfg := &Config{DecisionEnabled: true, DecisionVoiceWork: true, DecisionLocalModel: id, DecisionDevice: "cpu"}
		m, err := ManifestFor(cfg, AssetRequest{NeedDecision: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
		model, _ := decider.ModelFor(id)
		weights := 0
		for _, a := range m.Assets {
			if strings.HasPrefix(a.Source, "https://huggingface.co/") {
				weights++
				if a.Source != "https://huggingface.co/"+model.NativeRepo || a.License != model.License {
					t.Fatal("wrong model assets")
				}
			}
			if model.Engine == "liquid" && a.ID == "decision.runtime.source" {
				t.Fatal("Liquid downloads Decider source")
			}
		}
		if weights != 1 {
			t.Fatal("multiple weight sets selected")
		}
		root := t.TempDir()
		states := m.Status(root)
		found := false
		for _, state := range states {
			if state.ID == "decision.runtime.environment" {
				found = true
				if state.Installed || len(state.Missing) != 1 || state.Missing[0] != decider.NativePath(root, "cpu") {
					t.Fatal("status used wrong runtime profile")
				}
			}
		}
		if !found {
			t.Fatal("missing environment status")
		}
	}
}
