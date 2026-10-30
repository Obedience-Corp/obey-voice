package cmd

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/spf13/cobra"
)

func newDecisionSetupCmd() *cobra.Command {
	return &cobra.Command{Use: "decision [status|install]", Short: "Verify or install the selected decision model without enabling routing", Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs), ValidArgs: []string{"status", "install"}, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = config.DecisionSetupConfig(cfg)
		report, err := config.DecisionSetupStatus(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), report)
		if args[0] == "status" {
			return nil
		}
		err = config.EnsureRuntimeAssetsProgress(cmd.Context(), cfg, config.AssetRequest{NeedDecision: true}, func(p config.AssetProgress) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %.0f%%\n", p.Asset, p.Phase, p.Pct)
		})
		if err != nil {
			return fmt.Errorf("setup stopped: %w; choose Install / repair to retry (verified files are reused)", err)
		}
		report, err = config.DecisionSetupStatus(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), report)
		return nil
	}}
}
