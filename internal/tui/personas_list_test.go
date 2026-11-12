package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestPersonaListResolvesEffectiveModels(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	base := &config.Config{BrainProvider: "ollama", OllamaModel: "local-default:8b", GrokModel: "grok-default", CursorModel: "cursor-default", TTSProvider: "kokoro", TTSVoice: "af_heart"}
	for _, tc := range []struct {
		name                                         string
		brain                                        persona.Brain
		provider, model, providerSource, modelSource string
	}{
		{"inherited", persona.Brain{}, "ollama", "local-default:8b", "Settings", "Settings"},
		{"provider only", persona.Brain{Provider: "grok"}, "grok", "grok-default", "Persona", "Settings"},
		{"model only", persona.Brain{Model: "local-custom:14b"}, "ollama", "local-custom:14b", "Settings", "Persona"},
		{"both", persona.Brain{Provider: "cursor", Model: "chosen-model"}, "cursor", "chosen-model", "Persona", "Persona"},
		{"claude model", persona.Brain{Provider: "claude", Model: "haiku"}, "claude", "haiku", "Persona", "Persona"},
		{"claude without a model", persona.Brain{Provider: "claude"}, "claude", "provider default", "Persona", "Settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePersonaListStack(base, &persona.Profile{ID: "test", Brain: tc.brain})
			if got.provider != tc.provider || got.model != tc.model || got.providerSource != tc.providerSource || got.modelSource != tc.modelSource {
				t.Fatalf("stack = %+v", got)
			}
			if got.voice != "kokoro / af_heart" {
				t.Fatalf("inherited voice = %q", got.voice)
			}
			if got.note != "" {
				t.Fatalf("unexpected note: %+v", got)
			}
		})
	}
	if base.BrainProvider != "ollama" || base.OllamaModel != "local-default:8b" {
		t.Fatal("resolving rows mutated Settings")
	}
}

func TestPersonaListLoadsSettingsInsteadOfActiveOverlay(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("brain_provider: ollama\nollama_model: settings-model:8b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &persona.Profile{Schema: persona.Schema, ID: "inherit", DisplayName: "Inherited", Prompts: persona.PromptRefs{Persona: "obey"}}
	if err := persona.Write(p, false); err != nil {
		t.Fatal(err)
	}
	m := newPersonas(&config.Config{BrainProvider: "grok", GrokModel: "active-persona-override"})
	m.width, m.height = 100, 24
	view := stripANSI(m.View())
	if !strings.Contains(view, "settings-model:8b") || strings.Contains(view, "active-persona-override") {
		t.Fatalf("wrong effective model:\n%s", view)
	}
	// A later reload must pick up Settings changes, not keep stale stack rows.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("brain_provider: ollama\nollama_model: updated-model:14b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if !strings.Contains(stripANSI(m.View()), "updated-model:14b") {
		t.Fatal("reload kept stale model")
	}
}

func TestPersonaListUnavailableDefaultsDoNotBorrowActiveModel(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	m := newPersonas(&config.Config{BrainProvider: "ollama", OllamaModel: "wrong-active-model"})
	m.loadDefaults = func() (*config.Config, error) { return nil, errors.New("broken settings") }
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "inherited"}, {ID: "explicit", Brain: persona.Brain{Provider: "grok", Model: "explicit-model"}}}, nil
	}
	m.reload()
	m.width, m.height = 100, 24
	view := stripANSI(m.View())
	for _, want := range []string{"Settings unavailable", "unavailable", "explicit-model"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "wrong-active-model") {
		t.Fatal("inherited active overlay after Settings error")
	}
}

func TestPersonaListFitsAndScrollsAcrossTerminalSizes(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, size := range []struct{ w, h int }{{120, 32}, {80, 24}, {60, 18}, {42, 24}, {40, 12}, {80, 8}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m := newPersonas(&config.Config{ActivePersona: "persona-0"})
			m.loadDefaults = func() (*config.Config, error) {
				return &config.Config{BrainProvider: "ollama", OllamaModel: "qwen-local:8b", TTSProvider: "kokoro", TTSVoice: "af_heart"}, nil
			}
			m.listPersonas = func() ([]*persona.Profile, error) {
				var items []*persona.Profile
				for i := 0; i < 22; i++ {
					items = append(items, &persona.Profile{ID: fmt.Sprintf("persona-%d", i), DisplayName: fmt.Sprintf("Persona %02d", i)})
				}
				return items, nil
			}
			m.reload()
			m, _ = m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			// Live conversation selection, not the global default, owns the active marker.
			m.selectedPersona = func() string { return "persona-1" }
			view := stripANSI(m.View())
			if !strings.Contains(view, "✓ Persona 01") || strings.Contains(view, "✓ Persona 00") {
				t.Fatalf("wrong active marker:\n%s", view)
			}
			for i := 0; i <= len(m.items); i++ {
				view = stripANSI(m.View())
				// Filling the last terminal row left stale list text when opening the editor.
				if len(strings.Split(view, "\n")) > size.h-1 {
					t.Fatalf("height overflow:\n%s", view)
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > size.w {
						t.Fatalf("width overflow: %q", line)
					}
				}
				want := fmt.Sprintf("Persona %02d", m.cursor)
				if m.cursor == len(m.items) {
					want = personasCreateLabel
				}
				if !strings.Contains(view, want) {
					t.Fatalf("selection %q scrolled off:\n%s", want, view)
				}
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
			m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			if !strings.Contains(stripANSI(m.View()), personasCreateLabel) {
				t.Fatal("resize lost selected create row")
			}
		})
	}
}

func TestPersonaListBuiltinBadgeAndLongModel(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	for _, builtin := range []bool{true, false} {
		p := &persona.Profile{ID: persona.DefaultID, DisplayName: "Obey", Builtin: builtin}
		m := personasModel{width: 80, height: 24, items: []*persona.Profile{p}, stacks: map[string]personaListStack{}}
		m.stacks[p.ID] = personaListStack{provider: "ollama", model: "a-long-model-name-with-distinguishing-quantization-Q4_K_M:latest", providerSource: "Settings", modelSource: "Settings", voice: "kokoro / af_heart"}
		view := stripANSI(m.View())
		if strings.Contains(view, "built-in default") != builtin {
			t.Fatalf("incorrect ownership badge:\n%s", view)
		}
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(view, "│", "")), ""), "Q4_K_M:latest") {
			t.Fatalf("long model suffix hidden:\n%s", view)
		}
	}
}

func TestPersonaListMarksHostedBackends(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	base := &config.Config{BrainProvider: "ollama", OllamaModel: "local:8b", TTSProvider: "kokoro", TTSVoice: "af_heart"}
	for provider, want := range map[string]bool{"claude": true, "codex": true, "muse-spark": true, "ollama": false, "": false} {
		got := resolvePersonaListStack(base, &persona.Profile{ID: "test", Brain: persona.Brain{Provider: provider}})
		if got.hosted != want {
			t.Errorf("provider %q: hosted = %v, want %v", provider, got.hosted, want)
		}
	}
	m := newPersonas(base)
	m.width, m.height = 100, 24
	m.items = []*persona.Profile{{ID: "mira", DisplayName: "Mira", Brain: persona.Brain{Provider: "claude"}}, {ID: "ember", DisplayName: "Ember"}}
	m.reloadStacks()
	var hosted []string
	for _, line := range m.listLines(20) {
		if strings.Contains(ansi.Strip(line), "[hosted]") {
			hosted = append(hosted, ansi.Strip(line))
		}
	}
	if len(hosted) != 1 || !strings.Contains(hosted[0], "Mira") {
		t.Fatalf("hosted rows = %q", hosted)
	}
}
