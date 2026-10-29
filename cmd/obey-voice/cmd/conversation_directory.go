//go:build !integration

package cmd

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func resolveConversationDirectory(cfg *config.Config, personaID, override string, saved *session.Session) error {
	useDefaults := personaID == appTUI.StartDefaultsID
	if useDefaults {
		personaID = persona.DefaultID
	}
	if personaID == "" && saved != nil {
		personaID = saved.SelectedPersonaID
	}
	if personaID == "" {
		personaID = persona.ActiveID(cfg)
	}
	next := cfg.Clone()
	var state *config.ConversationDirectories
	if saved != nil {
		state = saved.Directories.Clone()
	}
	if state == nil {
		// A running server may currently be in another persona's directory.
		// Recover its launch default before resolving a new/legacy conversation.
		base := cfg.Clone()
		if base.ConversationDirectories != nil {
			base.WorkDir = base.ConversationDirectories.Default.Dir
			base.WorkDirSource = base.ConversationDirectories.Default.Source
		}
		info, err := config.ResolveSessionDirectory(base, override, "", "", "")
		if err != nil {
			return err
		}
		state = &config.ConversationDirectories{Default: config.ConversationDirectory{Dir: info.Dir, Source: string(info.Source)}, Personas: make(map[string]config.ConversationDirectory)}
		if saved != nil && saved.WorkDir != "" {
			// Legacy files have only one directory. It belongs to the saved
			// responder; a persona-sourced path must not become everyone's default.
			owner := saved.SelectedPersonaID
			if owner == "" {
				owner = personaID
			}
			state.Personas[owner] = config.ConversationDirectory{Dir: saved.WorkDir, Source: saved.WorkDirSource}
			if saved.WorkDirSource != "persona" {
				state.Default = state.Personas[owner]
			}
		}
	}
	next.ConversationDirectories = state
	var directory string
	if _, pinned := state.Personas[personaID]; !pinned {
		profile, err := persona.Load(personaID)
		if err != nil && saved == nil && !(useDefaults && errors.Is(err, fs.ErrNotExist)) {
			return fmt.Errorf("resolve persona directory: %w", err)
		}
		// Legacy transcripts with a removed persona remain readable; admission
		// still requires choosing a valid responder.
		if profile != nil {
			directory = profile.StartingDirectory
		}
	}
	if err := config.BindPersonaDirectory(next, personaID, directory); err != nil {
		return err
	}
	cfg.WorkDir, cfg.WorkDirSource = next.WorkDir, next.WorkDirSource
	cfg.ConversationDirectories = next.ConversationDirectories
	return nil
}
