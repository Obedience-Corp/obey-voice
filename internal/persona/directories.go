package persona

import (
	"fmt"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// UpdateStartingDirectory validates and persists one persona's starting
// directory. Empty clears it back to the app default. Nothing is written when
// the path is rejected.
func UpdateStartingDirectory(id, directory string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	normalized, err := config.ValidateStartingDirectory(directory)
	if err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	if p.StartingDirectory == normalized {
		return p, nil
	}
	p.StartingDirectory = normalized
	if err := Write(p, false); err != nil {
		return nil, fmt.Errorf("saving persona starting directory: %w", err)
	}
	return p, nil
}
