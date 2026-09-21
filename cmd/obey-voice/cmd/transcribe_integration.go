//go:build integration

package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

// newTranscribeCmd preserves the command tree in the CGO-free integration
// binary. File transcription needs the native sherpa/audio stack, so it is
// covered by the host build and its command tests.
func newTranscribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transcribe",
		Short: "Transcribe a finished WAV and stream timed utterances as JSONL",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("transcribe is unavailable in the CGO-free integration build")
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "capabilities",
		Short: "Report what this install can transcribe (read-only, offline)",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("transcribe capabilities is unavailable in the CGO-free integration build")
		},
	})
	return cmd
}

func init() {
	rootCmd.AddCommand(newTranscribeCmd())
}
