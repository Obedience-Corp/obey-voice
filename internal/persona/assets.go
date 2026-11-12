package persona

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"

// AssetSources is every persona's effective config, for code that must know
// which models a persona uses.
func AssetSources(cfg *config.Config) ([]config.PersonaAssets, error) {
	profiles, err := List()
	if err != nil {
		return nil, err
	}
	out := make([]config.PersonaAssets, 0, len(profiles))
	for _, p := range profiles {
		if p == nil {
			continue
		}
		derived := *cfg
		Apply(&derived, p)
		out = append(out, config.PersonaAssets{ID: p.ID, Cfg: &derived})
	}
	return out, nil
}
