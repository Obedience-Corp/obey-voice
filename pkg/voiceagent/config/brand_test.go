package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
)

func TestAppSlugIsObeyVoice(t *testing.T) {
	if AppSlug != "obey-voice" {
		t.Fatalf("AppSlug = %q, want obey-voice", AppSlug)
	}
}

func TestAppSlugSpellsAppName(t *testing.T) {
	if want := strings.ReplaceAll(strings.ToLower(AppName), " ", "-"); AppSlug != want {
		t.Fatalf("AppSlug = %q, want %q from AppName %q", AppSlug, want, AppName)
	}
}

func TestMacAppProcessIsAppName(t *testing.T) {
	if talkhotkey.MacAppProcess != AppName {
		t.Fatalf("talkhotkey.MacAppProcess = %q, want AppName %q", talkhotkey.MacAppProcess, AppName)
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
