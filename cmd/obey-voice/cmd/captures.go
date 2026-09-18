package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func newCapturesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captures",
		Short: "List sealed conversation recordings",
		Long:  "List Talk/Lab capture bundles under this install's captures directory.",
		Args:  cobra.NoArgs,
		RunE:  runCapturesList,
	}
	cmd.Flags().Bool("json", false, "Output machine-readable JSON")
	return cmd
}

func runCapturesList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	items, err := capture.List(config.CapturesDir())
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}
	if len(items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No captures.")
		return nil
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tMODE\tSTARTED\tSEALED\tDIR")
	for _, m := range items {
		sealed := ""
		if m.SealedAt != nil {
			sealed = m.SealedAt.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.SessionID, m.Mode, m.StartedAt.Local().Format(time.RFC3339), sealed, m.Dir)
	}
	return tw.Flush()
}

func init() {
	rootCmd.AddCommand(newCapturesCmd())
}
