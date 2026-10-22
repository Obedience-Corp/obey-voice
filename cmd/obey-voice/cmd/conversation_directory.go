//go:build !integration

package cmd

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func resolveConversationDirectory(cfg *config.Config, personaID, override string, saved *session.Session) error {
	var directory, savedDir, savedSource string
	if saved != nil {
		savedDir, savedSource = saved.WorkDir, saved.WorkDirSource
	}
	if personaID != appTUI.StartDefaultsID && savedDir == "" {
		if personaID == "" && saved != nil {
			personaID = saved.SelectedPersonaID
		}
		if personaID == "" {
			personaID = persona.ActiveID(cfg)
		}
		profile, err := persona.Load(personaID)
		if err != nil && saved == nil {
			return fmt.Errorf("resolve persona directory: %w", err)
		}
		// Legacy transcripts with a removed persona remain readable; selection
		// admission still requires the user to choose a valid responder.
		if profile != nil {
			directory = profile.StartingDirectory
		}
	}
	info, err := config.ResolveSessionDirectory(cfg, override, directory, savedDir, savedSource)
	if err != nil {
		return fmt.Errorf("persona %q: %w", personaID, err)
	}
	cfg.WorkDir, cfg.WorkDirSource = info.Dir, string(info.Source)
	return nil
}
