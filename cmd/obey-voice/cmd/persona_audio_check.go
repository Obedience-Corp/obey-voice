package cmd

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/spf13/cobra"
)

func personaAudioCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check-audio <id>",
		Short: "Quietly check saved native audio settings without activating capture",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			asJSON, _ := cmd.Flags().GetBool("json")
			result, err := checkPersonaAudio(cmd, args[0])
			if err != nil {
				if asJSON {
					return emitJSONError(cmd, err)
				}
				return err
			}
			if asJSON {
				return encodeJSON(cmd, result)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Native audio setup passed. Experimental responses may be inaccurate or claim actions that did not occur.")
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Emit setup status and runtime identity")
	return cmd
}

func checkPersonaAudio(cmd *cobra.Command, id string) (*nativeaudio.SetupResult, error) {
	cfg, err := config.LoadRaw()
	if err != nil {
		return nil, err
	}
	p, err := loadPersonaForCLI(id)
	if err != nil {
		return nil, err
	}
	// Check an inactive native draft without modifying its saved mode or settings.
	draft := *p
	audio := persona.Audio{Mode: persona.AudioNative}
	if p.Audio != nil {
		audio = *p.Audio
		audio.Mode = persona.AudioNative
	}
	draft.Audio = &audio
	candidate, effective, err := persona.ResolveAudioConfig(cfg, &draft)
	if err != nil {
		return nil, err
	}
	client, err := nativeaudio.NewClient(effective.Endpoint, effective.Model)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	requirements := nativeaudio.Requirements{
		ASR:   effective.Controls == persona.AudioControlsStandard || effective.Transcript == persona.AudioTranscriptRequired,
		Tools: effective.Controls == persona.AudioControlsStandard,
	}
	_, closeSpeech, result, err := nativeaudio.NewSupportCache().Prepare(cmd.Context(), client, candidate, requirements)
	if err != nil {
		return nil, err
	}
	defer closeSpeech()
	return &result, nil
}
