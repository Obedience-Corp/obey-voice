package personaruntime

import (
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestCatalogBindingUsesBaseForEmptyPersonaFields(t *testing.T) {
	a := testProfile("a", "A", "a")
	a.Brain.Model = "persona-a-model"
	a.TTS.Voice = "persona-a-voice"
	b := persona.Profile{Schema: persona.Schema, ID: "b", DisplayName: "B", Prompts: persona.PromptRefs{Persona: "b"}}
	f := newSelectorTestFixture(t, a, b)
	defer f.close(t)

	bindingA, err := f.catalog.ResolveBinding(f.base, "a")
	if err != nil {
		t.Fatal(err)
	}
	bindingB, err := f.catalog.ResolveBinding(f.base, "b")
	if err != nil {
		t.Fatal(err)
	}
	if got := bindingA.Config(); got.OllamaModel != "persona-a-model" || got.TTSVoice != "persona-a-voice" {
		t.Fatalf("A config = model %q voice %q, want persona overrides", got.OllamaModel, got.TTSVoice)
	}
	if got := bindingB.Config(); got.OllamaModel != f.base.OllamaModel || got.TTSVoice != f.base.TTSVoice || got.BrainProvider != f.base.BrainProvider {
		t.Fatalf("B empty-field config = model %q voice %q provider %q, want base model %q voice %q provider %q", got.OllamaModel, got.TTSVoice, got.BrainProvider, f.base.OllamaModel, f.base.TTSVoice, f.base.BrainProvider)
	}
}
