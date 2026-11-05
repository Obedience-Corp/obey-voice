//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
)

func TestPersonaListBrainAndStyling(t *testing.T) {
	personaEnv(t)
	writePersona(t, &persona.Profile{
		Schema: persona.Schema, ID: "other", DisplayName: "Other", Inactive: true,
		Brain:   persona.Brain{Worker: persona.Worker{Provider: "claude", Model: "worker-model"}},
		Prompts: persona.PromptRefs{Persona: "other"},
	})
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	var out bytes.Buffer
	cmd := newPersonaCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Samantha", "ollama / qwen2.5:14b", "claude / worker-model", "default", "kokoro / af_heart", "active", "inactive", "\x1b["} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in output: %q", want, out.String())
		}
	}
}

func TestPersonaListJSONIncludesBrain(t *testing.T) {
	personaEnv(t)
	var out bytes.Buffer
	cmd := newPersonaCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID       string        `json:"id"`
		Provider string        `json:"provider"`
		Brain    persona.Brain `json:"brain"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, out.String())
	}
	for _, row := range rows {
		if row.ID == "samantha" {
			if row.Provider != "kokoro" || row.Brain.Provider != "ollama" || row.Brain.Model != "qwen2.5:14b" {
				t.Fatalf("unexpected row: %+v", row)
			}
			return
		}
	}
	t.Fatal("samantha missing from list")
}
