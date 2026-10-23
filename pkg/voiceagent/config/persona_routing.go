package config

import "time"

const (
	DefaultPersonaRoutingTimeout = 120 // seconds, includes model queue/load time
	MaxPersonaRoutingTimeout     = 300
)

// PersonaRoutingTimeoutOf supplies the default for older config files and
// embedded hosts, and keeps even an out-of-range raw config finitely bounded.
func PersonaRoutingTimeoutOf(cfg *Config) time.Duration {
	seconds := DefaultPersonaRoutingTimeout
	if cfg != nil && cfg.PersonaRoutingTimeout > 0 {
		seconds = min(cfg.PersonaRoutingTimeout, MaxPersonaRoutingTimeout)
	}
	return time.Duration(seconds) * time.Second
}
