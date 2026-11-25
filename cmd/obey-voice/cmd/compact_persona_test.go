//go:build !integration

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

const compactDefaultOpening = "Summarize this entire conversation so far"

type compactRecordingBrain struct {
	*servePersonaTestBrain
	inputs *[]string
}

func (b compactRecordingBrain) ThinkFull(_ context.Context, input string, _ brain.StreamOptions) (string, error) {
	*b.inputs = append(*b.inputs, input)
	return "The user is planning a garden.", nil
}

func (compactRecordingBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, errors.New("no reply in this test")
}

func writeNamedTestPersona(t *testing.T, id, name string) {
	t.Helper()
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: id, DisplayName: name,
		TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: persona.PromptRefs{Persona: id},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := persona.WriteSystemPrompt(id, "You are {agent_name}."); err != nil {
		t.Fatal(err)
	}
}

func juniperHistory() []brain.Turn {
	var turns []brain.Turn
	for _, text := range []string{"Plant tomatoes.", "Add basil.", "Water at dawn."} {
		turns = append(turns,
			brain.Turn{Role: "user", Content: text},
			brain.Turn{Role: "assistant", Content: "Noted.", PersonaID: "juniper", PersonaDisplayName: "Juniper"})
	}
	return turns
}

func compactInstructions(inputs []string) []string {
	var out []string
	for _, in := range inputs {
		if strings.HasPrefix(in, compactDefaultOpening) {
			out = append(out, in)
		}
	}
	return out
}

func TestCompactNamesThePersonaBoundWhenItRuns(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeNamedTestPersona(t, "juniper", "Juniper")
	writeNamedTestPersona(t, "basil", "Basil")
	cfg := &config.Config{ActivePersona: "juniper", BrainProvider: "ollama", TTSProvider: "kokoro", MaxHistory: 2}

	var inputs []string
	record := func(c *config.Config) (brain.Provider, error) {
		b, err := serveBrainForConfig(c)
		if err != nil {
			return nil, err
		}
		return compactRecordingBrain{servePersonaTestBrain: b.(*servePersonaTestBrain), inputs: &inputs}, nil
	}
	s := newSwitcherForTest(t, cfg)
	s.newBrain = record
	initial, err := persona.ResolveBinding(cfg, "juniper")
	if err != nil {
		t.Fatal(err)
	}
	first, err := record(initial.Config())
	if err != nil {
		t.Fatal(err)
	}
	first.LoadHistory(juniperHistory())
	s.pipeline.Brain = first
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.selector.Close)
	bus := events.NewBus()
	wireCompact(s.pipeline, cfg, s.boundConfig, s.sessions.sess, bus)

	if _, err := s.apply("basil"); err != nil {
		t.Fatalf("switch to basil: %v", err)
	}
	if err := s.pipeline.CompactConversation(t.Context()); err != nil {
		t.Fatalf("manual compact: %v", err)
	}
	s.pipeline.Brain.LoadHistory(juniperHistory())
	_ = s.pipeline.RunTurnTextMode(t.Context(), "What should I plant next?")

	got := compactInstructions(inputs)
	if len(got) != 2 {
		t.Fatalf("compact instructions = %d (%q), want manual and automatic", len(got), inputs)
	}
	for i, instruction := range got {
		if !strings.Contains(instruction, "as Basil.") || strings.Contains(instruction, "as Juniper") {
			t.Fatalf("compaction %d named the wrong persona: %q", i, instruction)
		}
	}
}

func TestCompactFallbackNeverBorrowsAnotherPersonasPrompt(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeNamedTestPersona(t, "juniper", "Juniper")
	writeNamedTestPersona(t, "basil", "Basil")
	doc := filepath.Join(config.PromptsDir(), "compact", "garden.yaml")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema: obey-voice.prompt.v1\nprompt:\n  name: garden\n  kind: compact\n  system_prompt: |-\n    " + compactDefaultOpening + " as {agent_name}.\n"
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{ActivePersona: "juniper", BrainProvider: "ollama", TTSProvider: "kokoro", CompactPrompt: "garden"}

	var inputs []string
	record := func(c *config.Config) (brain.Provider, error) {
		b, err := serveBrainForConfig(c)
		if err != nil {
			return nil, err
		}
		b.LoadHistory(juniperHistory())
		return compactRecordingBrain{servePersonaTestBrain: b.(*servePersonaTestBrain), inputs: &inputs}, nil
	}
	s := newSwitcherForTest(t, cfg)
	s.newBrain = record
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.selector.Close)
	s.pipeline.Brain.LoadHistory(juniperHistory())
	wireCompact(s.pipeline, cfg, s.boundConfig, s.sessions.sess, events.NewBus())
	if err := os.Remove(doc); err != nil {
		t.Fatal(err)
	}

	if _, err := s.apply("basil"); err != nil {
		t.Fatalf("switch to basil: %v", err)
	}
	if err := s.pipeline.CompactConversation(t.Context()); err == nil || !strings.Contains(err.Error(), "garden") {
		t.Fatalf("compact for a persona that never resolved = %v, want the resolution error", err)
	}
	if _, err := s.apply("juniper"); err != nil {
		t.Fatalf("switch back to juniper: %v", err)
	}
	if err := s.pipeline.CompactConversation(t.Context()); err != nil {
		t.Fatalf("compact with juniper's last instruction: %v", err)
	}
	got := compactInstructions(inputs)
	if len(got) != 1 || !strings.Contains(got[0], "as Juniper.") {
		t.Fatalf("compact instructions = %q, want only juniper's last one", got)
	}
}

func TestCompactPromptFailureDisablesCompactVisibly(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeNamedTestPersona(t, "juniper", "Juniper")
	cfg := &config.Config{ActivePersona: "juniper", BrainProvider: "ollama", TTSProvider: "kokoro", CompactPrompt: "missing-compact"}
	s := newSwitcherForTest(t, cfg)
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.selector.Close)
	s.pipeline.Brain.LoadHistory(juniperHistory())
	bus := events.NewBus()
	var reported []string
	bus.SubscribeAll(func(e events.Event) {
		if failure, ok := e.(events.Error); ok && failure.Stage == "compact" {
			reported = append(reported, failure.Message)
		}
	})

	wireCompact(s.pipeline, cfg, s.boundConfig, s.sessions.sess, bus)

	if len(reported) != 1 || !strings.Contains(reported[0], "/compact disabled") {
		t.Fatalf("compact errors = %q, want one disabled notice", reported)
	}
	if err := s.pipeline.CompactConversation(t.Context()); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("compact after a failed prompt = %v, want not configured", err)
	}
}
