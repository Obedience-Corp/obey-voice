//go:build integration

package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

// newSTTCmd preserves the command tree in the CGO-free integration binary.
// Running local STT requires the native sherpa/audio stack and is covered by
// the host build and fixture tests.
func newSTTCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stt",
		Short: "Inspect what speech-to-text does with your words",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("stt is unavailable in the CGO-free integration build")
		},
	}
}

func init() {
	rootCmd.AddCommand(newSTTCmd())
}
