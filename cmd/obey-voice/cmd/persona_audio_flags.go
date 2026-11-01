package cmd

import (
	"reflect"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/spf13/cobra"
)

func addPersonaAudioFlags(cmd *cobra.Command) {
	cmd.Flags().String("audio-mode", "", "Per-persona audio mode: standard or native (experimental)")
	cmd.Flags().String("audio-provider", "", "Native runtime provider: ollama")
	cmd.Flags().String("audio-model", "", "Native audio model (default lfm3-audio:2b)")
	cmd.Flags().String("audio-endpoint", "", "Native Ollama origin or proxy prefix (empty inherits saved Ollama origin)")
	cmd.Flags().String("audio-voice", "", "Native model voice (empty uses runtime default)")
	cmd.Flags().String("audio-controls", "", "Spoken controls: standard or conversation")
	cmd.Flags().String("audio-transcript", "", "Input transcription: auto, required or off")
	cmd.Flags().Bool("retain-audio", false, "Keep input recordings privately with this session")
}
func mergePersonaAudio(cmd *cobra.Command, previous *persona.Audio) (*persona.Audio, []string, error) {
	next := &persona.Audio{}
	if previous != nil {
		*next = *previous
	}
	changed := []string{}
	for _, field := range []struct {
		flag, path string
		value      *string
	}{
		{"audio-mode", "audio.mode", &next.Mode}, {"audio-provider", "audio.provider", &next.Provider},
		{"audio-model", "audio.model", &next.Model}, {"audio-endpoint", "audio.endpoint", &next.Endpoint},
		{"audio-voice", "audio.voice", &next.Voice}, {"audio-controls", "audio.controls", &next.Controls},
		{"audio-transcript", "audio.transcript", &next.Transcript},
	} {
		if cmd.Flags().Changed(field.flag) {
			value, _ := cmd.Flags().GetString(field.flag)
			if value != *field.value {
				*field.value = value
				changed = append(changed, field.path)
			}
		}
	}
	if cmd.Flags().Changed("retain-audio") {
		value, _ := cmd.Flags().GetBool("retain-audio")
		if value != next.RetainAudio {
			next.RetainAudio = value
			changed = append(changed, "audio.retain_audio")
		}
	}
	if err := next.Validate(); err != nil {
		return nil, nil, codedError(codeInvalidInput, "%v", err)
	}
	if previous == nil && *next == (persona.Audio{}) {
		return nil, nil, nil
	}
	if reflect.DeepEqual(previous, next) {
		return previous, nil, nil
	}
	return next, changed, nil
}
