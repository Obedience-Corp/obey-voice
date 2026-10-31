//go:build integration

package cmd

import "github.com/spf13/cobra"

// The integration binary intentionally excludes all speech/device runtimes.
// Keep the command discoverable while making that unavailable capability explicit.
func personaAudioCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check-audio <id>",
		Short: "Quietly check saved native audio settings without activating capture",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			err := codedError("runtime_unavailable", "native audio setup is unavailable in the integration test binary")
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return emitJSONError(cmd, err)
			}
			return err
		},
	}
	cmd.Flags().Bool("json", false, "Emit setup status and runtime identity")
	addPersonaAudioFlags(cmd)
	return cmd
}
