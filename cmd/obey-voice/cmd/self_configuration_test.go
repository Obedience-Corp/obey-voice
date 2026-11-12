package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// Exercise the shipped skill's create/inspect/edit/verify workflow against a
// disposable install. No model, microphone or operator configuration is used.
func TestSelfConfigurationSkillPersonaWorkflow(t *testing.T) {
	root := personaEnv(t)
	catalog, err := (skills.Loader{Builtins: true}).Catalog(t.Context())
	if err != nil || len(catalog) != 1 {
		t.Fatalf("shipped skill unavailable: %v", err)
	}
	prompt := filepath.Join(root, "helper.txt")
	body := "You are Workshop Helper. Explain woodworking clearly and ask before changing plans."
	if err := os.WriteFile(prompt, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPersona(t, "create", "Workshop Helper", "--prompt-file", prompt, "--no-activate", "--json")
	if err != nil {
		t.Fatal(err)
	}
	created := decodeCreateResult(t, out)
	if !created.Created || created.Activated {
		t.Fatalf("create=%+v", created)
	}
	if _, err = runPersona(t, "edit", created.Persona.ID, "--brain-provider", "ollama", "--brain-model", "lfm3:1.7b", "--json"); err != nil {
		t.Fatal(err)
	}
	out, err = runPersona(t, "show", created.Persona.ID, "--with-prompt", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var saved personaResultJSON
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Persona.Brain.Model != "lfm3:1.7b" || saved.Prompt == nil || saved.Prompt.Body == nil || !strings.Contains(*saved.Prompt.Body, body) {
		t.Fatalf("saved=%s", out)
	}
	cfg, err := config.LoadRaw()
	if err != nil || cfg.ActivePersona != "samantha" {
		t.Fatalf("creation changed default: %+v %v", cfg, err)
	}
}

// A flag the skill names that no command accepts sends a worker into an error
// loop, so every --flag in the shipped skill must exist somewhere in the CLI.
func TestSelfConfigurationSkillNamesOnlyRealFlags(t *testing.T) {
	known := map[string]bool{"help": true}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) { known[f.Name] = true })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { known[f.Name] = true })
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	named := regexp.MustCompile(`--([a-z][a-z0-9-]*)`).FindAllStringSubmatch(skills.ApplicationInstructions(), -1)
	if len(named) == 0 {
		t.Fatal("the skill names no flags")
	}
	for _, m := range named {
		if !known[m[1]] {
			t.Errorf("the skill names --%s, which no command accepts", m[1])
		}
	}
}
