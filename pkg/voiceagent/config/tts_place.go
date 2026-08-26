package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Where speech synthesis runs. This is topology, not a provider: tts_provider
// still names the engine. See workflow/design/obey-voice-fast-local-fleet
// spec/10 §2.
const (
	TTSPlaceLocal = "local"
	TTSPlaceLAN   = "lan"
)

// DefaultTTSLanTimeoutMS bounds the dial and the obey-speech/v1 handshake, and
// nothing else. A cold speech-serve spends seconds loading its model before it
// answers; that wait belongs to the daemon, not to this timeout.
const DefaultTTSLanTimeoutMS = 200

// ttsPlaceEnum is owned here: place is a config-level topology with no upstream
// package to source it from.
func ttsPlaceEnum() []string { return []string{TTSPlaceLocal, TTSPlaceLAN} }

// TTSPlaceOf normalizes cfg's place. Empty is local, so an install that predates
// the key keeps speaking on this computer.
func TTSPlaceOf(cfg *Config) string {
	if cfg == nil {
		return TTSPlaceLocal
	}
	switch place := strings.ToLower(strings.TrimSpace(cfg.TTSPlace)); place {
	case "":
		return TTSPlaceLocal
	default:
		return place
	}
}

// TTSLanTimeoutMSOf returns the dial/handshake budget, falling back to the
// default when unset or nonsensical.
func TTSLanTimeoutMSOf(cfg *Config) int {
	if cfg == nil || cfg.TTSLanTimeoutMS <= 0 {
		return DefaultTTSLanTimeoutMS
	}
	return cfg.TTSLanTimeoutMS
}

// ValidateTTSPlace rejects a speech topology that cannot be dialed safely.
// Plaintext off-box is a config error, not a runtime surprise: the token would
// cross the LAN in the clear.
func ValidateTTSPlace(cfg *Config) error {
	place := TTSPlaceOf(cfg)
	switch place {
	case TTSPlaceLocal:
		return nil
	case TTSPlaceLAN:
	default:
		return fmt.Errorf("unsupported tts_place %q; want %s or %s", place, TTSPlaceLocal, TTSPlaceLAN)
	}
	raw := strings.TrimSpace(cfg.TTSWorkerURL)
	if raw == "" {
		return fmt.Errorf("tts_place %s requires tts_worker_url (https://host:7263)", TTSPlaceLAN)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("tts_worker_url %q is not a URL: %w", raw, err)
	}
	if parsed.User != nil {
		return fmt.Errorf("tts_worker_url must not carry credentials; set tts_worker_token instead")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("tts_worker_url %q has no host", raw)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if !isLoopbackHost(host) {
			return fmt.Errorf("tts_worker_url %q is plaintext to a non-loopback host; use https://%s", raw, parsed.Host)
		}
		return nil
	default:
		return fmt.Errorf("tts_worker_url %q must be https:// (or http:// on loopback)", raw)
	}
}

// isLoopbackHost reports whether host names this machine. Only a literal
// loopback address or localhost qualifies: a name that resolves to 127.0.0.1
// today may not tomorrow, and the check gates plaintext.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
