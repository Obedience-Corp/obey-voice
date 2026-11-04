package config

// Clone copies settings and their slice-backed values for an immutable runtime binding.
func (cfg *Config) Clone() *Config {
	if cfg == nil {
		return nil
	}
	c := *cfg
	c.ConversationDirectories = cfg.ConversationDirectories.Clone()
	c.VoiceWorkers = cfg.VoiceWorkers.Clone()
	c.PersonaWorkerTiers = cfg.PersonaWorkerTiers.Clone()
	c.STTCustomWords = append([]string(nil), cfg.STTCustomWords...)
	c.NameSummonAliases = append([]string(nil), cfg.NameSummonAliases...)
	c.PersonaDisplayNames = append([]string(nil), cfg.PersonaDisplayNames...)
	c.SkillsDisabled = append([]string(nil), cfg.SkillsDisabled...)
	c.Meeting.Route.Destinations = append([]MeetingDestinationConfig(nil), cfg.Meeting.Route.Destinations...)
	for i := range c.Meeting.Route.Destinations {
		c.Meeting.Route.Destinations[i].Tags = append([]string(nil), cfg.Meeting.Route.Destinations[i].Tags...)
	}
	return &c
}
