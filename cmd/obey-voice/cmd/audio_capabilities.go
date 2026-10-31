//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audiojob"
	"github.com/spf13/cobra"
)

func newAudioCapabilitiesCmd() *cobra.Command {
	var configPath string
	var jsonOutput bool
	command := &cobra.Command{Use: "audio-capabilities", Short: "Verify an explicit local audio pack and report independent task readiness", Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(command *cobra.Command, _ []string) error {
		command.SilenceUsage = true
		if configPath == "" || !jsonOutput {
			return fmt.Errorf("audio-capabilities requires --audio-config and --json")
		}
		var config audiojob.Config
		if err := readAudioDocument(configPath, &config); err != nil {
			return err
		}
		if config.Managed == nil {
			return fmt.Errorf("audio-capabilities requires the explicit editor-owned pack")
		}
		ctx, cancel := signalContext()
		defer cancel()
		ctx, timeout := context.WithTimeout(ctx, 2*time.Minute)
		defer timeout()
		runtime, provenance, err := audiojob.StartManaged(ctx, config)
		if err != nil {
			return err
		}
		defer runtime.Close()
		return json.NewEncoder(command.OutOrStdout()).Encode(runtime.Capabilities(config.Runtime, provenance))
	}}
	command.Flags().StringVar(&configPath, "audio-config", "", "Absolute isolated obey-voice.audio-config/v1 JSON")
	command.Flags().BoolVar(&jsonOutput, "json", false, "Write one obey-audio-capabilities/v1 document")
	return command
}
