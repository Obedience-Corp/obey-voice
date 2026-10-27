//go:build !integration

package cmd

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type DesktopVoiceSelection = persona.TTS

// PreviewDesktopVoice uses app defaults plus the unsaved persona speech fields.
// LoadRaw avoids applying or seeding the active persona just to hear a sample.
func PreviewDesktopVoice(ctx context.Context, selection DesktopVoiceSelection) error {
	cfg, err := config.LoadRaw()
	if err != nil {
		return err
	}
	return tui.PreviewPersonaVoice(ctx, cfg, selection)
}
