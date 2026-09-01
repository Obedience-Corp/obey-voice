//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Piped stdin must route to the plain app.Run loop, never the full-screen
// TUI (D3: app.Run stays the driver for non-TTY invocations).
func TestStdinIsTerminalFalseForPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if stdinIsTerminal() {
		t.Fatal("a pipe must not be detected as a terminal")
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"claude", config.Config{BrainProvider: "claude"}, "claude"},
		{"grok with model", config.Config{BrainProvider: "grok", GrokModel: "grok-4"}, "grok-4"},
		{"grok default", config.Config{BrainProvider: "grok"}, "grok"},
		{"ollama", config.Config{BrainProvider: "ollama", OllamaModel: "llama3"}, "llama3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelName(&tt.cfg); got != tt.want {
				t.Errorf("modelName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Use defaults must read Settings, not the active persona overlay. Otherwise
// the first picker row would start Uncle Fu's pinned stack after the user
// just set grok in Settings.
func TestLoadConfigForSessionDefaultsSkipsPersonaOverlay(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(""+
		"brain_provider: grok\n"+
		"grok_model: grok-build\n"+
		"active_persona: research\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &persona.Profile{
		Schema:      persona.Schema,
		ID:          "research",
		DisplayName: "Research",
		Brain:       persona.Brain{Provider: "ollama", Model: "llama3"},
		TTS:         persona.TTS{Provider: "kokoro", Voice: "af_heart"},
		Prompts:     persona.PromptRefs{Persona: "research"},
	}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}

	overlaid, err := loadConfigForSession("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if overlaid.BrainProvider != "ollama" {
		t.Fatalf("active overlay = %q, want ollama from the persona", overlaid.BrainProvider)
	}

	settings, err := loadConfigForSession(appTUI.StartDefaultsID)
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if settings.BrainProvider != "grok" {
		t.Fatalf("Use defaults = %q, want grok from Settings", settings.BrainProvider)
	}
}
