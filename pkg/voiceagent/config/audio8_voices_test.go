package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAudio8VoiceName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "voice name is required"},
		{"blank", "   ", "voice name is required"},
		{"a parent traversal", "../escape", "invalid voice name"},
		{"a nested path", "a/b", "invalid voice name"},
		{"a leading dot hides it from the listing", ".hidden", "invalid voice name"},
		{"uppercase would render differently in each surface", "Alice", "invalid voice name"},
		{"a space", "my voice", "invalid voice name"},
		{"too long", strings.Repeat("a", 65), "invalid voice name"},
		{"the packaged voice", Audio8PackagedVoice, "reserved"},

		{name: "a simple name", in: "alice"},
		{name: "dots dashes and underscores", in: "alice.2_a-b"},
		{name: "a digit start", in: "2nd-take"},
		{name: "the longest allowed", in: strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAudio8VoiceName(tt.in)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateAudio8VoiceName(%q) = %v, want nil", tt.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateAudio8VoiceName(%q) = %v, want %q", tt.in, err, tt.want)
			}
		})
	}
}

func TestAudio8RegisteredVoiceNames(t *testing.T) {
	modelsDir := t.TempDir()
	if got := Audio8RegisteredVoiceNames(modelsDir); len(got) != 0 {
		t.Fatalf("an empty models dir lists %v", got)
	}
	if got := Audio8RegisteredVoiceNames(""); got != nil {
		t.Fatalf("Audio8RegisteredVoiceNames(\"\") = %v, want nil", got)
	}

	write := func(name string, withMeta bool) {
		t.Helper()
		dir := filepath.Join(Audio8VoicesDir(modelsDir), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if withMeta {
			if err := os.WriteFile(filepath.Join(dir, Audio8VoiceMetaFile), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("zoe", true)
	write("alice", true)
	// A half-written registration and a stray file are not voices.
	write(".alice.staging", true)
	write("nometa", false)
	if err := os.WriteFile(filepath.Join(Audio8VoicesDir(modelsDir), "loose.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := Audio8RegisteredVoiceNames(modelsDir)
	if strings.Join(got, ",") != "alice,zoe" {
		t.Errorf("Audio8RegisteredVoiceNames() = %v, want [alice zoe] in lexical order", got)
	}
}

// TestRegisteredAudio8VoicesStayClaimedWhateverTheProvider is the deliberate
// asymmetry with the export beside them: the export is claimed only when audio8
// is selected because it is 1.6 GB that can be fetched again, while a
// registered voice is kilobytes encoded from a recording the user may no longer
// have. Switching provider must never offer someone's voice for deletion.
func TestRegisteredAudio8VoicesStayClaimedWhateverTheProvider(t *testing.T) {
	modelsDir := t.TempDir()
	want := Audio8VoicesDir(modelsDir)
	for _, provider := range []string{"kokoro", "qwen3-tts", Audio8ProviderName, ""} {
		cfg := &Config{TTSProvider: provider}
		claims, err := configReferencedClaims(cfg, modelsDir)
		if err != nil {
			t.Fatalf("configReferencedClaims(%q): %v", provider, err)
		}
		found := false
		for _, claim := range claims {
			for _, own := range claim.own {
				if own == want {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("tts_provider %q left the registered voice store unclaimed", provider)
		}
	}
}

func TestAudio8VoiceCountDetail(t *testing.T) {
	modelsDir := t.TempDir()
	register := func(name string) {
		t.Helper()
		dir := filepath.Join(Audio8VoicesDir(modelsDir), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, Audio8VoiceMetaFile), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := audio8VoiceCountDetail(modelsDir); got != "1 voice (packaged reference only)" {
		t.Errorf("with no registrations doctor says %q", got)
	}
	register("alice")
	if got := audio8VoiceCountDetail(modelsDir); got != "2 voices (packaged reference + 1 registered)" {
		t.Errorf("with one registration doctor says %q", got)
	}
	register("bob")
	if got := audio8VoiceCountDetail(modelsDir); got != "3 voices (packaged reference + 2 registered)" {
		t.Errorf("with two registrations doctor says %q", got)
	}
}

func TestAudio8VoicesDirFrom(t *testing.T) {
	cfg := &Config{ModelsDir: filepath.Join("custom", "models")}
	want := filepath.Join("custom", "models", Audio8VoicesSubdir)
	if got := Audio8VoicesDirFrom(cfg); got != want {
		t.Errorf("Audio8VoicesDirFrom() = %q, want %q", got, want)
	}
	// The store is a sibling of the export, never inside it: the export is
	// routinely a symlink into a read-only snapshot.
	export := filepath.Join("custom", "models", Audio8Subdir)
	if strings.HasPrefix(Audio8VoicesDirFrom(cfg), export+string(filepath.Separator)) {
		t.Error("the voice store lives inside the export, which may be read-only")
	}
}
