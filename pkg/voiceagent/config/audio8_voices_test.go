package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalAudio8VoiceName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
		want    string
	}{
		{name: "empty", in: "", wantErr: "voice name is required"},
		{name: "blank", in: "   ", wantErr: "voice name is required"},
		{name: "a parent traversal", in: "../escape", wantErr: "invalid voice name"},
		{name: "a nested path", in: "a/b", wantErr: "invalid voice name"},
		{name: "a leading dot hides it from the listing", in: ".hidden", wantErr: "invalid voice name"},
		{name: "uppercase would render differently in each surface", in: "Alice", wantErr: "invalid voice name"},
		{name: "an interior space", in: "my voice", wantErr: "invalid voice name"},
		{name: "too long", in: strings.Repeat("a", 65), wantErr: "invalid voice name"},
		{name: "the packaged voice", in: Audio8PackagedVoice, wantErr: "reserved"},
		{name: "the packaged voice, padded", in: "  " + Audio8PackagedVoice + "  ", wantErr: "reserved"},

		{name: "a simple name", in: "alice", want: "alice"},
		{name: "dots dashes and underscores", in: "alice.2_a-b", want: "alice.2_a-b"},
		{name: "a digit start", in: "2nd-take", want: "2nd-take"},
		{name: "the longest allowed", in: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},

		// Surrounding whitespace is stripped, not refused: resolveVoice trims
		// the requested name, so the trimmed spelling is the only one that can
		// ever be spoken. Storing " bob " would list and select fine and then
		// fail at synthesis forever.
		{name: "surrounding spaces are stripped", in: "  bob  ", want: "bob"},
		{name: "a trailing newline is stripped", in: "bob\n", want: "bob"},
		{name: "a trailing tab is stripped", in: "bob\t", want: "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalAudio8VoiceName(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("CanonicalAudio8VoiceName(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("CanonicalAudio8VoiceName(%q) = %q on error, want empty", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalAudio8VoiceName(%q) = %v, want nil", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalAudio8VoiceName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// The canonical form has to be a fixed point, or the round trip
			// through config and back is not stable.
			again, err := CanonicalAudio8VoiceName(got)
			if err != nil || again != got {
				t.Errorf("CanonicalAudio8VoiceName(%q) = %q, %v; want it unchanged", got, again, err)
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
	// Nor is a hand-made directory whose name is not already canonical: it
	// would list and be selectable, and then never resolve.
	write(" bob ", true)
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
