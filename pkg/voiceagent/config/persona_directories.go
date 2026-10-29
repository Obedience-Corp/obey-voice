package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"
)

// ConversationDirectory is a canonical tool directory and its provenance.
type ConversationDirectory struct {
	Dir    string `json:"dir"`
	Source string `json:"source"`
}

// ConversationDirectories keeps the launch default separate from each
// responder's pinned directory. It is session state, not an installation setting.
type ConversationDirectories struct {
	Default  ConversationDirectory            `json:"default"`
	Personas map[string]ConversationDirectory `json:"personas,omitempty"`
}

func (d *ConversationDirectories) Clone() *ConversationDirectories {
	if d == nil {
		return nil
	}
	return &ConversationDirectories{Default: d.Default, Personas: maps.Clone(d.Personas)}
}

// BindPersonaDirectory resolves a persona once per conversation, before its
// brain, tools, skills and worker are constructed. It never changes process cwd.
func BindPersonaDirectory(cfg *Config, id, startingDirectory string) error {
	state := cfg.ConversationDirectories.Clone()
	if state == nil {
		info := WorkDirInfoFrom(cfg)
		state = &ConversationDirectories{Default: ConversationDirectory{Dir: info.Dir, Source: string(info.Source)}}
	}
	choice, pinned := state.Personas[id]
	if !pinned {
		choice = state.Default
		// Explicit launch overrides apply to every responder. Otherwise use
		// this persona's choice, or the captured default (not the current env).
		if choice.Source != "flag" && choice.Source != "env" && strings.TrimSpace(startingDirectory) != "" {
			choice = ConversationDirectory{Dir: expandHomePath(strings.TrimSpace(startingDirectory)), Source: "persona"}
		}
	}
	if choice.Dir == "" || !filepath.IsAbs(choice.Dir) {
		return fmt.Errorf("persona %q: directory %q must be absolute", id, choice.Dir)
	}
	dir, err := ValidateStartingDirectory(choice.Dir)
	if err != nil {
		return fmt.Errorf("persona %q %s directory: %w; restore this folder or start a new conversation with a valid starting directory", id, choice.Source, err)
	}
	choice.Dir = dir
	if choice.Source == "" {
		choice.Source = "session"
	}

	if state.Personas == nil {
		state.Personas = make(map[string]ConversationDirectory)
	}
	state.Personas[id] = choice
	cfg.ConversationDirectories = state
	cfg.WorkDir, cfg.WorkDirSource = choice.Dir, choice.Source
	return nil
}
