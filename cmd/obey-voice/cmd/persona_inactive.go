package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// personaSetInactiveCmd changes only the profile's catalog state. Profiles
// remain loadable and editable while inactive; persona.Use and runtime
// selection enforce the enabled-only rule in the persona package.
func personaSetInactiveCmd(inactive bool) *cobra.Command {
	verb := "enable"
	short := "Enable a persona for selection"
	if inactive {
		verb = "disable"
		short = "Disable a persona from selection"
	}
	cmd := &cobra.Command{
		Use:   verb + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			asJSON, _ := cmd.Flags().GetBool("json")
			result, err := setPersonaInactive(args[0], inactive)
			if err != nil {
				if asJSON {
					return emitJSONError(cmd, err)
				}
				return err
			}
			if asJSON {
				return encodeJSON(cmd, result)
			}
			state := "enabled"
			if inactive {
				state = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Persona %s: %s\n", result.Persona.ID, state)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}

func setPersonaInactive(id string, inactive bool) (*personaResultJSON, error) {
	cfg, err := loadPersonaManagementConfig()
	if err != nil {
		return nil, err
	}
	if _, err := loadPersonaForCLI(id); err != nil {
		return nil, err
	}
	p, err := persona.SetInactive(cfg, id, inactive)
	if err != nil {
		return nil, err
	}
	return &personaResultJSON{
		Persona: p,
		Changed: []string{"inactive"},
		Active:  p.ID == persona.ActiveID(cfg),
		Applies: defaultApplies(),
	}, nil
}

// Keep management and recovery available if a file edit disabled the saved
// default. Normal loads still seed shipped profiles and resolve migrations.
func loadPersonaManagementConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if errors.Is(err, persona.ErrInactive) || errors.Is(err, persona.ErrNativeAudioUnavailable) {
		return config.LoadRaw()
	}
	return cfg, err
}

// Persona repair/setup must remain reachable even when the saved default is a
// native draft that ordinary conversation startup cannot yet activate.
func loadCommandConfig(cmd *cobra.Command) (*config.Config, error) {
	if cmd.Parent() != nil && cmd.Parent().Name() == "persona" {
		return loadPersonaManagementConfig()
	}
	return config.Load()
}
