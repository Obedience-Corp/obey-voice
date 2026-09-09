package tui

import (
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// sttModel is a settings model whose writes are recorded and whose PATH
// lookups are answered by the test, so a picker never depends on what happens
// to be installed on the machine running it.
func sttModel(t *testing.T, cfg *config.Config, installed ...string) (*settingsModel, map[string]any) {
	t.Helper()
	if cfg.ModelsDir == "" {
		cfg.ModelsDir = t.TempDir()
	}
	saved := map[string]any{}
	m := &settingsModel{
		cfg: cfg,
		saveConfig: func(key string, value any) error {
			saved[key] = value
			return nil
		},
		lookPath: func(name string) (string, error) {
			for _, have := range installed {
				if have == name {
					return "/usr/local/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
	}
	m.buildSTTItems()
	return m, saved
}

func sttRow(t *testing.T, m *settingsModel, provider string) (int, string) {
	t.Helper()
	for i, item := range m.sttItems {
		if item.provider == provider {
			return i, item.label
		}
	}
	t.Fatalf("no row for %q: %+v", provider, m.sttItems)
	return 0, ""
}

func selectSTT(t *testing.T, m *settingsModel, provider string) {
	t.Helper()
	idx, _ := sttRow(t, m, provider)
	m.selectSTTItem(m.sttItems[idx])
}

// Every recognizer this build has is offered, exactly once, under the name
// config stores — a compound alias is a spelling, not a row.
func TestSTTItemsListEveryProviderOnce(t *testing.T) {
	m, _ := sttModel(t, &config.Config{}, "whisper-cli", "transcribe-cli")

	seen := map[string]int{}
	for _, item := range m.sttItems {
		seen[item.provider]++
	}
	for _, want := range []string{
		config.STTProviderParakeet, config.STTProviderSherpa,
		config.STTProviderWhisperCPP, config.STTProviderTranscribeCPP,
	} {
		if seen[want] != 1 {
			t.Fatalf("provider %q appears %d times: %+v", want, seen[want], m.sttItems)
		}
	}
	for _, alias := range []string{"sherpa-parakeet", "sherpa-offline", "sherpa-streaming"} {
		if seen[alias] != 0 {
			t.Fatalf("compound alias %q is a row: %+v", alias, m.sttItems)
		}
	}

	// Every provider the factory advertises reaches a row, so a new one is
	// listed without touching this screen.
	for _, spec := range stt.Providers() {
		norm, ok := config.NormalizeSTT(spec.Name)
		if !ok {
			t.Fatalf("factory advertises %q, which config cannot normalize", spec.Name)
		}
		if seen[norm.Provider] == 0 {
			t.Fatalf("factory provider %q has no row", spec.Name)
		}
	}
}

func TestSelectSTTWritesCanonicalNames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pick     string
		wantMode string
	}{
		{name: "parakeet", pick: config.STTProviderParakeet, wantMode: config.STTModeOffline},
		{name: "sherpa", pick: config.STTProviderSherpa, wantMode: config.STTModeOffline},
		{name: "whispercpp", pick: config.STTProviderWhisperCPP, wantMode: config.STTModeCLI},
		{name: "transcribecpp", pick: config.STTProviderTranscribeCPP, wantMode: config.STTModeOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, saved := sttModel(t, &config.Config{STTProvider: "sherpa-parakeet"}, "whisper-cli", "transcribe-cli")

			selectSTT(t, m, tc.pick)

			if saved["stt_provider"] != tc.pick {
				t.Fatalf("stt_provider = %v, want %q", saved["stt_provider"], tc.pick)
			}
			if saved["stt_mode"] != tc.wantMode {
				t.Fatalf("stt_mode = %v, want %q", saved["stt_mode"], tc.wantMode)
			}
			if m.cfg.STTProvider != tc.pick || m.cfg.STTMode != tc.wantMode {
				t.Fatalf("live config = %q/%q", m.cfg.STTProvider, m.cfg.STTMode)
			}
			// Whatever was written must resolve; the picker cannot produce a
			// pair the next conversation refuses.
			if _, err := config.NormalizeSTTWithMode(m.cfg.STTProvider, m.cfg.STTMode); err != nil {
				t.Fatalf("saved pair does not resolve: %v", err)
			}
		})
	}
}

// A streaming user who opens the picker and re-picks their own recognizer must
// not be quietly moved to utterance-final.
func TestSelectSTTPreservesStreamingMode(t *testing.T) {
	m, saved := sttModel(t, &config.Config{STTProvider: "sherpa-streaming"})

	_, label := sttRow(t, m, config.STTProviderSherpa)
	if !strings.Contains(label, config.STTModeStreaming) {
		t.Fatalf("sherpa row = %q, want it to show the mode in effect", label)
	}

	selectSTT(t, m, config.STTProviderSherpa)
	if saved["stt_provider"] != config.STTProviderSherpa || saved["stt_mode"] != config.STTModeStreaming {
		t.Fatalf("saved=%v, want the streaming mode preserved", saved)
	}
}

// The canonical pair, not just the compound alias: this is what the picker
// itself writes, so a second open+re-pick must not demote streaming.
func TestSelectSTTPreservesStreamingModeFromCanonicalPair(t *testing.T) {
	m, saved := sttModel(t, &config.Config{STTProvider: "sherpa", STTMode: config.STTModeStreaming})

	_, label := sttRow(t, m, config.STTProviderSherpa)
	if !strings.Contains(label, config.STTModeStreaming) {
		t.Fatalf("sherpa row = %q, want it to show the mode in effect", label)
	}

	selectSTT(t, m, config.STTProviderSherpa)
	if saved["stt_provider"] != config.STTProviderSherpa || saved["stt_mode"] != config.STTModeStreaming {
		t.Fatalf("saved=%v, want the streaming mode preserved", saved)
	}
}

// The inverse: a mode left over from another recognizer must not survive into
// a pair that cannot resolve.
func TestSelectSTTClearsAStaleMode(t *testing.T) {
	m, saved := sttModel(t, &config.Config{STTProvider: "sherpa", STTMode: config.STTModeStreaming})

	selectSTT(t, m, config.STTProviderParakeet)

	if saved["stt_mode"] != config.STTModeOffline {
		t.Fatalf("stt_mode = %v, want the new provider's own mode", saved["stt_mode"])
	}
	if _, err := config.NormalizeSTTWithMode(m.cfg.STTProvider, m.cfg.STTMode); err != nil {
		t.Fatalf("stale mode survived: %v", err)
	}
}

func TestSTTLegacyAliasIsShownInNormalizedForm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		active   string
		note     []string
	}{
		{name: "sherpa-offline", provider: "sherpa-offline", active: config.STTProviderSherpa, note: []string{"sherpa-offline", "sherpa", "offline"}},
		{name: "sherpa-parakeet", provider: "sherpa-parakeet", active: config.STTProviderParakeet, note: []string{"sherpa-parakeet", "parakeet"}},
		{name: "sherpa-streaming", provider: "sherpa-streaming", active: config.STTProviderSherpa, note: []string{"sherpa-streaming", "streaming"}},
		{name: "canonical name has nothing to explain", provider: "parakeet", active: config.STTProviderParakeet},
		{name: "default has nothing to explain", provider: "", active: config.STTProviderParakeet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{STTProvider: tc.provider, ModelsDir: t.TempDir()}
			if got := activeSTTProvider(cfg); got != tc.active {
				t.Fatalf("active = %q, want %q", got, tc.active)
			}
			note := sttConfiguredAliasNote(cfg)
			if len(tc.note) == 0 {
				if note != "" {
					t.Fatalf("note = %q, want none", note)
				}
				return
			}
			for _, want := range tc.note {
				if !strings.Contains(note, want) {
					t.Fatalf("note = %q, want it to mention %q", note, want)
				}
			}
		})
	}
}

func TestSTTUnknownConfiguredProviderIsNamed(t *testing.T) {
	note := sttConfiguredAliasNote(&config.Config{STTProvider: "whisper-turbo"})
	if !strings.Contains(note, "whisper-turbo") {
		t.Fatalf("note = %q, want it to name the unusable value", note)
	}
}

// The message must not promise something the runtime does not do: ReloadVoice
// rebuilds TTS only, and STT is constructed with the microphone when a
// conversation starts.
func TestSelectSTTMessageIsHonestAboutRestart(t *testing.T) {
	m, _ := sttModel(t, &config.Config{})

	selectSTT(t, m, config.STTProviderSherpa)

	if !strings.Contains(m.message, "new conversation") {
		t.Fatalf("message = %q, want it to say when the change applies", m.message)
	}
	for _, overclaim := range []string{"applies immediately", "applies now", "took effect"} {
		if strings.Contains(strings.ToLower(m.message), overclaim) {
			t.Fatalf("message overclaims (%q): %s", overclaim, m.message)
		}
	}
}

// The voice fingerprint drives ReloadVoice, which cannot rebuild STT. Adding
// the recognizer to it would trigger a reload that silently does nothing.
func TestSTTIsNotWiredIntoTheVoiceReload(t *testing.T) {
	base := &config.Config{TTSProvider: "kokoro", STTProvider: "sherpa", STTMode: config.STTModeOffline}
	switched := *base
	switched.STTProvider = config.STTProviderParakeet
	switched.STTMode = config.STTModeOffline

	if voiceSettingsState(base) != voiceSettingsState(&switched) {
		t.Fatal("the recognizer is in the voice fingerprint, which would promise a live reload that does not happen")
	}
}

// A recognizer that cannot run here is still a legitimate thing to configure:
// it saves, and says what is missing rather than refusing.
func TestSTTEnvironmentGapsWarnAndStillSave(t *testing.T) {
	t.Run("binary missing", func(t *testing.T) {
		m, saved := sttModel(t, &config.Config{})

		_, label := sttRow(t, m, config.STTProviderTranscribeCPP)
		if !strings.Contains(label, "transcribe-cli") || !strings.Contains(label, "PATH") {
			t.Fatalf("row = %q, want the missing binary named before it is picked", label)
		}

		selectSTT(t, m, config.STTProviderTranscribeCPP)
		if saved["stt_provider"] != config.STTProviderTranscribeCPP {
			t.Fatalf("an environment gap must not block the save: %v", saved)
		}
		if !strings.Contains(m.message, "transcribecpp_binary") {
			t.Fatalf("message = %q, want the config key that fixes it", m.message)
		}
	})

	t.Run("weights missing", func(t *testing.T) {
		m, saved := sttModel(t, &config.Config{}, "transcribe-cli")

		_, label := sttRow(t, m, config.STTProviderTranscribeCPP)
		if !strings.Contains(label, "weights") {
			t.Fatalf("row = %q, want the missing weights named", label)
		}

		selectSTT(t, m, config.STTProviderTranscribeCPP)
		if saved["stt_provider"] != config.STTProviderTranscribeCPP {
			t.Fatalf("an environment gap must not block the save: %v", saved)
		}
		if !strings.Contains(m.message, "models ensure") {
			t.Fatalf("message = %q, want the command that fixes it", m.message)
		}
	})

	t.Run("whispercpp binary missing", func(t *testing.T) {
		m, _ := sttModel(t, &config.Config{})
		_, label := sttRow(t, m, config.STTProviderWhisperCPP)
		if !strings.Contains(label, "whisper-cli") {
			t.Fatalf("row = %q, want the missing binary named", label)
		}
	})

	t.Run("nothing missing is silent", func(t *testing.T) {
		cfg := &config.Config{ModelsDir: t.TempDir()}
		asset, err := config.TranscribeCPPModel("")
		if err != nil {
			t.Fatal(err)
		}
		path := asset.ModelPath(cfg.ModelsDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
		m, _ := sttModel(t, cfg, "transcribe-cli", "whisper-cli")

		for _, provider := range []string{config.STTProviderTranscribeCPP, config.STTProviderWhisperCPP} {
			_, label := sttRow(t, m, provider)
			if strings.Contains(label, "PATH") || strings.Contains(label, "weights") {
				t.Fatalf("row = %q, want no warning when nothing is missing", label)
			}
		}
	})
}

// Settings copy names software, never the hardware it runs on.
func TestSTTCopyNamesSoftwareNotHardware(t *testing.T) {
	m, _ := sttModel(t, &config.Config{}, "whisper-cli", "transcribe-cli")

	var joined string
	for _, item := range m.sttItems {
		joined += item.label + "\n"
	}
	for _, banned := range []string{"NVIDIA", "RTX", "5060", "CUDA", "Metal", "GPU"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("STT copy names hardware (%q): %s", banned, joined)
		}
	}
}

// The tab is reachable and renders its own rows.
func TestSTTSectionRendersRows(t *testing.T) {
	m, _ := sttModel(t, &config.Config{STTProvider: "sherpa-offline"}, "whisper-cli", "transcribe-cli")
	m.section = sectionSTT
	m.width, m.height = 120, 30

	// The recognizer is one row; its options live in the picker.
	if got := m.currentListLen(); got != 1 {
		t.Fatalf("list length = %d, want the one Recognizer row", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "STT") {
		t.Fatalf("the STT tab is not in the tab strip:\n%s", view)
	}
	// The row shows the active recognizer and explains the stored alias.
	for _, want := range []string{"Recognizer", config.STTProviderSherpa, "sherpa-offline"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view is missing %q:\n%s", want, view)
		}
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view = stripANSI(next.View())
	for _, provider := range []string{config.STTProviderParakeet, config.STTProviderSherpa, config.STTProviderTranscribeCPP} {
		if !strings.Contains(view, provider) {
			t.Fatalf("picker is missing %q:\n%s", provider, view)
		}
	}
	if !strings.Contains(view, "✓") {
		t.Fatalf("picker does not mark the active recognizer:\n%s", view)
	}
}
