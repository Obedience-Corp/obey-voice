package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// configSchemaPayload is the contract behind `obey-voice config schema --json`:
// every setting a front end can render, with the copy, bounds, enum values and
// restart truth obey-voice itself uses. Adding a config key needs no front-end
// change because the front end renders this, not a hand-written list.
type configSchemaPayload struct {
	SchemaVersion int              `json:"schema_version"`
	ConfigFile    string           `json:"config_file"`
	Groups        []string         `json:"groups"`
	Keys          []config.KeySpec `json:"keys"`
}

// newConfigSchemaCmd builds the schema subcommand. A constructor rather than a
// package var so its --json flag cannot leak between invocations.
func newConfigSchemaCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "schema [key]",
		Short: "Describe every configuration key, or one (read-only)",
		Long: "Describe every configuration key: its type, default, accepted values,\n" +
			"bounds, group, help text, and whether changing it needs an agent restart.\n" +
			"With a key, only that key is described, in the same payload shape; an\n" +
			"unknown key fails with the closest known keys.\n\n" +
			"--json emits one object for machine consumers. The Obey Voice app builds\n" +
			"its Settings screens from it, so a key added to obey-voice appears there\n" +
			"with no app change. Never fails on a broken config: the built-in defaults\n" +
			"are reported with a warning on stderr.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A load failure is not fatal here: LoadRaw only supplies the
			// config-dependent enums, and a user with a broken config is exactly
			// the one who needs the schema.
			cfg, _ := config.LoadRaw()
			payload := configSchemaPayload{
				SchemaVersion: config.SchemaVersion,
				ConfigFile:    config.ConfigFile(),
				Groups:        config.Groups(),
				Keys:          config.SchemaFor(cfg),
			}
			if len(args) == 1 {
				spec, ok := schemaKey(payload.Keys, args[0])
				if !ok {
					return failConfig(cmd, asJSON, &config.SetError{
						Code:       config.CodeUnknownKey,
						Key:        args[0],
						Message:    fmt.Sprintf("unknown config key %q", args[0]),
						DidYouMean: config.SuggestKeys(args[0]),
					})
				}
				payload.Groups, payload.Keys = []string{spec.Group}, []config.KeySpec{spec}
			}
			if asJSON {
				return writeJSON(cmd, payload)
			}
			printSchema(cmd, payload)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output machine-readable JSON")
	return cmd
}

// schemaKey finds key in keys.
func schemaKey(keys []config.KeySpec, key string) (config.KeySpec, bool) {
	for _, spec := range keys {
		if spec.Key == key {
			return spec, true
		}
	}
	return config.KeySpec{}, false
}

func printSchema(cmd *cobra.Command, payload configSchemaPayload) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", titleStyle.Render("Obey Voice Settings Schema"))
	fmt.Fprintf(out, "  %s\n", dimStyle.Render("Config file: "+payload.ConfigFile))
	for _, group := range payload.Groups {
		keys := keysInGroup(payload.Keys, group)
		if len(keys) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n  %s\n", titleStyle.Render(group))
		for _, spec := range keys {
			fmt.Fprintf(out, "  %s %s %s\n",
				keyStyle.Render(spec.Key),
				dimStyle.Render(string(spec.Type)),
				valueStyle.Render(spec.Title))
			fmt.Fprintf(out, "    %s\n", dimStyle.Render(spec.Help))
		}
	}
	fmt.Fprintln(out)
}

func keysInGroup(keys []config.KeySpec, group string) []config.KeySpec {
	var out []config.KeySpec
	for _, spec := range keys {
		if spec.Group == group {
			out = append(out, spec)
		}
	}
	return out
}
