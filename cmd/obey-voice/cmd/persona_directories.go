package cmd

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/spf13/cobra"
)

type personaDirectoryChoices struct {
	Campaigns []meeting.Campaign `json:"campaigns"`
	Notice    string             `json:"notice,omitempty"`
}

// Discovery failure is nonfatal: users can always enter a custom directory.
func discoverPersonaDirectories(ctx context.Context, run meeting.Runner, look meeting.LookPath) personaDirectoryChoices {
	result := personaDirectoryChoices{Campaigns: []meeting.Campaign{}}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	checkedRun := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		data, err := run(ctx, name, args...)
		if err == nil && !bytes.Contains(data, []byte("[")) {
			err = fmt.Errorf("camp returned no JSON campaign list")
		}
		return data, err
	}
	camps, err := meeting.ListCampaigns(ctx, checkedRun, look)
	if err != nil {
		result.Notice = "Cannot load registered camps: " + err.Error()
		return result
	}
	for _, c := range camps {
		if c.Path == "" {
			continue
		}
		path, err := config.ValidateStartingDirectory(c.Path)
		if err != nil {
			continue
		}
		c.Path = path
		result.Campaigns = append(result.Campaigns, c)
	}
	return result
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
