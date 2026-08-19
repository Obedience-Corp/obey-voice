//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/narrate"
)

func runNarratePrepare(cmd *cobra.Command, opts narrate.PrepareOptions) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Narrate prepare plan: %s (integration stub)\n", opts.PlanPath)
	return nil
}

func runNarrateRender(cmd *cobra.Command, opts narrate.RenderOptions) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Narrate render plan: %s (integration stub)\n", opts.PlanPath)
	return nil
}
