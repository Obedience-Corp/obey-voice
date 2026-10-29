//go:build !integration

package cmd

import (
	"context"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type DesktopVoiceSelection struct {
	Provider  string `json:"provider"`
	Voice     string `json:"voice"`
	Tier      string `json:"tier"`
	PersonaID string `json:"persona_id,omitempty"`
}

func desktopPreviewSelection(selection DesktopVoiceSelection) (persona.TTS, error) {
	if selection.PersonaID == "" {
		return persona.TTS{Provider: selection.Provider, Voice: selection.Voice, Tier: selection.Tier}, nil
	}
	if selection.Provider != "" || selection.Voice != "" || selection.Tier != "" {
		return persona.TTS{}, fmt.Errorf("choose a saved persona or draft voice fields, not both")
	}
	p, err := persona.Load(selection.PersonaID)
	if err != nil {
		return persona.TTS{}, fmt.Errorf("load preview persona: %w", err)
	}
	return p.TTS, nil
}

// PreviewDesktopVoice uses app defaults plus saved or unsaved persona speech fields.
// LoadRaw avoids applying or seeding the active persona just to hear a sample.
func PreviewDesktopVoice(ctx context.Context, selection DesktopVoiceSelection) error {
	cfg, err := config.LoadRaw()
	if err != nil {
		return err
	}
	speech, err := desktopPreviewSelection(selection)
	if err != nil {
		return err
	}
	return tui.PreviewPersonaVoice(ctx, cfg, speech)
}
