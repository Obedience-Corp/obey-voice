package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

// A flag the skill names that its command does not accept sends a worker into
// an error loop. Each backticked command in the shipped skill resolves to its
// subcommand, and every flag in it, or in the flag-only spans that follow it,
// must be one that subcommand accepts.
func TestSelfConfigurationSkillNamesOnlyRealFlags(t *testing.T) {
	var current *cobra.Command
	checked := 0
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(skills.ApplicationInstructions(), -1) {
		words := strings.Fields(m[1])
		cmd, resolved := rootCmd, false
		if len(words) > 0 && words[0] == "obey-voice" {
			words, resolved = words[1:], true
		}
		for _, word := range words {
			sub := subcommand(cmd, word)
			if sub == nil {
				break
			}
			cmd, resolved = sub, true
		}
		if resolved {
			current = cmd
		}
		for _, word := range words {
			if !strings.HasPrefix(word, "--") {
				continue
			}
			name := strings.SplitN(strings.TrimPrefix(word, "--"), "=", 2)[0]
			if current == nil {
				t.Errorf("--%s in %q follows no command", name, m[1])
				continue
			}
			if name != "help" && current.Flags().Lookup(name) == nil && current.InheritedFlags().Lookup(name) == nil {
				t.Errorf("the skill gives `%s` the flag --%s, which it does not accept", current.CommandPath(), name)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("the skill names no flags")
	}
}

// subcommand is cmd's child called name, or nil.
func subcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || sub.HasAlias(name) {
			return sub
		}
	}
	return nil
}
