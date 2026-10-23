package cmd

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/spf13/cobra"
)

type personaDirectoryChoices = campdirs.Choices

// Discovery failure is nonfatal: users can always enter a custom directory.
func discoverPersonaDirectories(ctx context.Context, run meeting.Runner, look meeting.LookPath) personaDirectoryChoices {
	return campdirs.Discover(ctx, run, look)
}

func personaDirectoriesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "directories", Short: "List registered local camp starting directories", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result := discoverPersonaDirectories(cmd.Context(), meeting.DefaultRunner, meeting.DefaultLookPath)
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				return encodeJSON(cmd, result)
			}
			for _, c := range result.Campaigns {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", c.Name, c.Path)
			}
			if result.Notice != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), result.Notice)
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Emit JSON")
	return cmd
}
