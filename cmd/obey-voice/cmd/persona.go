package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func init() {
	rootCmd.AddCommand(newPersonaCmd())
}

// newPersonaCmd builds the persona command tree fresh on every call. Cobra
// flag state is sticky per command instance, so constructing rather than
// sharing package-level vars keeps a test run from inheriting the previous
// run's --json or Changed() flags.
func newPersonaCmd() *cobra.Command {
	cmd := personaCmd()
	cmd.AddCommand(personaListCmd())
	cmd.AddCommand(personaDirectoriesCmd())
	cmd.AddCommand(personaShowCmd())
	cmd.AddCommand(personaAudioCheckCmd())
	cmd.AddCommand(personaUseCmd())
	cmd.AddCommand(personaCreateCmd())
	cmd.AddCommand(personaEditCmd())
	cmd.AddCommand(personaDeleteCmd())
	cmd.AddCommand(personaSetInactiveCmd(false))
	cmd.AddCommand(personaSetInactiveCmd(true))
	return cmd
}

func personaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "persona",
		Short: "List and switch voice agent personas",
		Long: `Manage voice agent personas (obey-voice multi-persona profiles).

Profiles live under:
  ~/.obey/agents/voice/<AppSlug>/personas/<id>/persona.yaml

The active persona overlays agent_name, persona prompt name, and tts_voice
on config load. Prompt bodies stay in the prompts catalog.`,
	}
}

func personaListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed persona profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadPersonaManagementConfig()
			if err != nil {
				return err
			}
			list, issues, err := persona.ListWithIssues()
			if err != nil {
				return err
			}
			for _, issue := range issues {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: persona %q is unavailable; repair its persona.yaml: %v\n", issue.ID, issue.Err)
			}
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				type row struct {
					Audio             *persona.Audio `json:"audio,omitempty"`
					Brain             persona.Brain  `json:"brain"`
					StartingDirectory string         `json:"starting_directory"`
					ID                string         `json:"id"`
					DisplayName       string         `json:"display_name"`
					Provider          string         `json:"provider,omitempty"`
					Voice             string         `json:"voice,omitempty"`
					Prompt            string         `json:"prompt"`
					Builtin           bool           `json:"builtin"`
					Active            bool           `json:"active"`
					Inactive          bool           `json:"inactive"`
					Hosted            bool           `json:"hosted"`
					Path              string         `json:"path"`
				}
				active := persona.ActiveID(cfg)
				out := make([]row, 0, len(list))
				for _, p := range list {
					out = append(out, row{
						Audio:             p.Audio,
						Brain:             p.Brain,
						StartingDirectory: p.StartingDirectory,
						ID:                p.ID,
						DisplayName:       p.DisplayName,
						Provider:          p.TTS.Provider,
						Voice:             p.TTS.Voice,
						Prompt:            p.Prompts.Persona,
						Builtin:           p.Builtin,
						Active:            p.ID == active,
						Inactive:          p.Inactive,
						Hosted:            personaHosted(cfg, p),
						Path:              p.Path,
					})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "  No personas found. Load config once to migrate defaults.")
				return nil
			}
			active := persona.ActiveID(cfg)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Personas"))
			for _, p := range list {
				status := ""
				if p.ID == active {
					status = " " + activeStyle.Render("active")
				}
				if p.Inactive {
					status += " " + dimStyle.Render("inactive")
				}
				fmt.Fprintf(out, "\n  %s %s%s\n", sectionStyle.Render(p.DisplayName), dimStyle.Render("("+p.ID+")"), status)
				hosted := ""
				if personaHosted(cfg, p) {
					hosted = " " + valueStyle.Render("[hosted]")
				}
				fmt.Fprintf(out, "    %s  %s / %s%s\n", keyStyle.Render("Brain "), personaListValue(p.Brain.Provider), personaListValue(p.Brain.Model), hosted)
				fmt.Fprintf(out, "    %s  %s / %s\n", keyStyle.Render("Worker"), personaListValue(p.Brain.Worker.Provider), personaListValue(p.Brain.Worker.Model))
				fmt.Fprintf(out, "    %s  %s / %s\n", keyStyle.Render("Voice "), personaListValue(p.TTS.Provider), personaListValue(p.TTS.Voice))
				fmt.Fprintf(out, "    %s  %s\n", keyStyle.Render("Prompt"), p.Prompts.Persona)
			}
			fmt.Fprintf(out, "\n  %s %s\n", dimStyle.Render("Profiles dir:"), persona.Dir())
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

// An empty override uses the applicable app or provider default.
func personaListValue(value string) string {
	if value == "" {
		return dimStyle.Render("default")
	}
	return value
}

func personaShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [id]",
		Short: "Show one persona profile (default: active)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runPersonaShow,
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.Flags().Bool("with-prompt", false, "Include the persona's system prompt document (and its body)")
	return cmd
}

func runPersonaShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	result, err := describePersona(cmd, args)
	if err != nil {
		if asJSON {
			return emitJSONError(cmd, err)
		}
		return err
	}
	if asJSON {
		return encodeJSON(cmd, result)
	}
	printPersonaShow(cmd, result)
	return nil
}

// describePersona reports one persona in the same envelope `persona edit`
// returns, so a caller decodes one shape for both verbs. --with-prompt adds
// the identity body an editor needs to load the text without ever parsing
// obey-voice.prompt.v1 itself.
func describePersona(cmd *cobra.Command, args []string) (*personaResultJSON, error) {
	cfg, err := loadPersonaManagementConfig()
	if err != nil {
		return nil, err
	}
	id := persona.ActiveID(cfg)
	if len(args) == 1 {
		id = args[0]
	}
	p, err := loadPersonaForCLI(id)
	if err != nil {
		return nil, err
	}
	result := &personaResultJSON{
		Persona: p,
		Changed: []string{},
		Active:  p.ID == persona.ActiveID(cfg),
		Applies: defaultApplies(),
	}
	if withPrompt, _ := cmd.Flags().GetBool("with-prompt"); withPrompt {
		doc, err := persona.DescribePrompt(p)
		if err != nil {
			return nil, err
		}
		body := doc.Body
		result.Prompt = &promptJSON{
			Name:       doc.Name,
			Path:       doc.Path,
			Source:     doc.Source,
			Structured: doc.Structured,
			Hash:       doc.Hash,
			Body:       &body,
		}
	}
	return result, nil
}

func printPersonaShow(cmd *cobra.Command, r *personaResultJSON) {
	out := cmd.OutOrStdout()
	p := r.Persona
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Persona: "+p.ID))
	fmt.Fprintf(out, "  display_name    %s\n", p.DisplayName)
	fmt.Fprintf(out, "  starting_directory %s\n", p.StartingDirectory)
	if p.ParticipationDescription != "" {
		fmt.Fprintf(out, "  participation_description %s\n", p.ParticipationDescription)
	}
	fmt.Fprintf(out, "  builtin         %v\n", p.Builtin)
	fmt.Fprintf(out, "  inactive        %v\n", p.Inactive)
	fmt.Fprintf(out, "  active          %v\n", r.Active)
	if p.Brain.Provider != "" || p.Brain.Model != "" {
		fmt.Fprintf(out, "  brain.provider  %s\n", p.Brain.Provider)
		fmt.Fprintf(out, "  brain.model     %s\n", p.Brain.Model)
	}
	fmt.Fprintf(out, "  tts.provider    %s\n", p.TTS.Provider)
	fmt.Fprintf(out, "  tts.voice       %s\n", p.TTS.Voice)
	if p.TTS.Tier != "" {
		fmt.Fprintf(out, "  tts.tier        %s\n", p.TTS.Tier)
	}
	fmt.Fprintf(out, "  prompts.persona %s\n", p.Prompts.Persona)
	if p.Prompts.Turn != "" {
		fmt.Fprintf(out, "  prompts.turn    %s\n", p.Prompts.Turn)
	}
	fmt.Fprintf(out, "  path            %s\n", p.Path)
	if r.Prompt != nil {
		fmt.Fprintf(out, "  prompt.source   %s\n", r.Prompt.Source)
		fmt.Fprintf(out, "  prompt.hash     %s\n", r.Prompt.Hash)
		if r.Prompt.Body != nil && *r.Prompt.Body != "" {
			fmt.Fprintf(out, "\n%s\n", *r.Prompt.Body)
		}
	}
	fmt.Fprintln(out)
}

// personaHosted reports whether the persona's effective brain, after it
// inherits the app default, sends conversation text off this machine.
func personaHosted(cfg *config.Config, p *persona.Profile) bool {
	effective := cfg.Clone()
	persona.Apply(effective, p)
	return brain.IsHosted(effective.BrainProvider)
}
