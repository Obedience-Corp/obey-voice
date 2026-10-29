package cmd

import (
	"encoding/json"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/spf13/cobra"
)

func newParticipationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "participation", Short: "Inspect or change the running conversation's agent participation", PersistentPreRunE: func(*cobra.Command, []string) error { return nil }}
	show := &cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		settings, err := talkhotkey.Participation(cmd.Context(), nil)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(settings)
	}}
	var memberList string
	set := &cobra.Command{Use: "set <off|invited|open>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		settings, err := talkhotkey.Participation(cmd.Context(), nil)
		if err != nil {
			return err
		}
		settings.Mode = args[0]
		if cmd.Flags().Changed("members") {
			settings.Members = nil
			if memberList != "all" {
				settings.Members = strings.Split(memberList, ",")
			}
		}
		if err := settings.Validate(); err != nil {
			return err
		}
		result, err := talkhotkey.Participation(cmd.Context(), &settings)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	set.Flags().StringVar(&memberList, "members", "all", "Conversation persona IDs separated by commas, or all enabled personas")
	cmd.AddCommand(show, set)
	return cmd
}
