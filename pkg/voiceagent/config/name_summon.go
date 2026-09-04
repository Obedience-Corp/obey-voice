package config

// NameSummonSelfTriggerRisk reports the one arrangement name summon must
// refuse: the gate reads the same finalized transcripts the microphone
// produces, so with no voice front-end cancelling playback the agent hears its
// own name in its own voice and summons itself (D5, R2 "Self-summon hazard").
//
// Same effective-front-end question as the barge-in echo diagnostic, and
// deliberately the same accessor: the two must never disagree about whether
// echo cancellation is running.
func NameSummonSelfTriggerRisk(cfg *Config) bool {
	if cfg == nil || !cfg.NameSummonEnabled {
		return false
	}
	return VoiceFrontendOf(cfg) == VoiceFrontendOff
}
