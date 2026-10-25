package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAppSlugIsObeyVoice(t *testing.T) {
	if AppSlug != "obey-voice" {
		t.Fatalf("AppSlug = %q, want obey-voice", AppSlug)
	}
}

func TestDefaultPathsUseAppSlug(t *testing.T) {
	cfg := DefaultConfigDir()
	if !strings.HasSuffix(cfg, filepath.Join("agents", "voice", AppSlug)) {
		t.Fatalf("DefaultConfigDir() = %q, want .../agents/voice/%s", cfg, AppSlug)
	}
	cache := DefaultCacheDir()
	if filepath.Base(cache) != AppSlug {
		t.Fatalf("DefaultCacheDir() = %q, want base %q", cache, AppSlug)
	}
	models := DefaultModelsDir()
	if !strings.HasSuffix(models, filepath.Join(AppSlug, "models")) {
		t.Fatalf("DefaultModelsDir() = %q, want .../%s/models", models, AppSlug)
	}
}
