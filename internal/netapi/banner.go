package netapi

// ProtocolVersion is the integer serve reports in GET /v1/status and the
// machine-readable ready banner so clients can gate features against a stable
// contract version.
//
// 2 adds the /v1/meeting capture surface (PROTOCOL_DELTAS D6); 3 adds meeting
// history (GET /v1/meetings, bundle-id resolution), the note control action,
// and route_plan at start (D7). Clients should still prefer the per-feature
// capability flags in GET /v1/status over the version number, since serve can
// be built or configured without a feature: capture needs meetings == true and
// protocol_version >= 2, history needs protocol_version >= 3.
const ProtocolVersion = 3

// Capability flag names. They are additive facts about what this build's
// /v1/stream contract carries, which is why they are flags and not a version
// bump: docs/serve-protocol.md's own rule is to "feature-detect by status
// rather than by protocol_version, which does not change for additive"
// surfaces. A client reads the flag it needs and degrades on an older serve,
// where the key is simply absent.
const (
	// CapResponseDeltaPayload: response_delta carries "text". Without it the
	// event arrives as a bare {"type":"response_delta"} and a client cannot
	// render a streaming reply.
	CapResponseDeltaPayload = "response_delta_payload"
	// CapThinkingDeltaPayload: thinking_delta carries "text". Without it the
	// event arrives as a bare {"type":"thinking_delta"} and a client cannot
	// render operator-facing reasoning.
	CapThinkingDeltaPayload = "thinking_delta_payload"
	// CapAudioLevelPayload: audio_level carries "source" and "level".
	CapAudioLevelPayload = "audio_level_payload"
	// CapInterruptEvents: a remote interrupt that cancelled something emits
	// turn_interrupted, and speaking_interrupted when TTS was in flight.
	CapInterruptEvents = "interrupt_events"
	// CapVoiceClaimOnDequeue: a voice_start queued behind a live turn claims
	// the mic when it is dequeued, not on arrival, so a queued client cannot
	// lock another one out.
	CapVoiceClaimOnDequeue = "voice_claim_on_dequeue"
)

// Capabilities is the flag set this build advertises, in the ready banner and
// in GET /v1/status. It is static: these are properties of the binary, not of
// how it was configured — configuration-dependent surfaces (meetings, personas)
// stay separate status fields.
func Capabilities() map[string]bool {
	return map[string]bool{
		CapResponseDeltaPayload: true,
		CapThinkingDeltaPayload: true,
		CapAudioLevelPayload:    true,
		CapInterruptEvents:      true,
		CapVoiceClaimOnDequeue:  true,
	}
}

// ReadyBanner is the single JSON line `serve --banner-json` writes to stdout
// once the listener is bound. A supervising process reads this line to learn
// the URL, credentials, and fingerprint instead of scraping the human banner.
// Fields reflect what is real for the current mode (e.g. Tailscale) but the
// key set stays stable.
type ReadyBanner struct {
	Event           string `json:"event"` // always "ready"
	ProtocolVersion int    `json:"protocol_version"`
	URL             string `json:"url"`
	Port            int    `json:"port"`
	Fingerprint     string `json:"fingerprint"`
	Token           string `json:"token"`
	MDNS            bool   `json:"mdns"`
	Tailscale       bool   `json:"tailscale"`
	PID             int    `json:"pid"`
	// Binds lists every bound host:port, primary first. It is always present
	// on a serve that reached OnListening, including single-bind serves, so a
	// client picks a reachable address instead of inferring one from URL
	// (ADR-008). URL stays the address remote clients should open.
	Binds []string `json:"binds,omitempty"`
	// Capabilities are the additive /v1/stream contract flags this build
	// carries, the same set GET /v1/status reports. Present so a supervising
	// process that only reads this line can gate a feature without opening a
	// socket first.
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	// WorkDir is the directory the agent's file and shell tools operate in, so
	// a client can show where a tool call would land. Always present: the
	// resolver behind it never yields an empty path, and GET /v1/status reports
	// the same field unconditionally.
	WorkDir string `json:"work_dir"`
	// WorkDirSource is the layer that decided WorkDir: flag, config, env,
	// launch-dir, home-fallback, or temp-fallback. Present with WorkDir because
	// the path alone cannot tell a supervising app that an environment variable
	// moved the agent somewhere nobody chose.
	WorkDirSource string `json:"work_dir_source"`
	// Campaign is the campaign workspace WorkDir sits in, with found=false when
	// it sits in none. Always present, so "not in a campaign" and "older serve
	// that does not report campaigns" stay distinguishable.
	Campaign CampaignInfo `json:"campaign"`
	// ClientSetupURL is present only in limited client-access mode (a Tailscale
	// serve that could not mint a trusted cert). Its presence means "limited";
	// its absence means full access.
	ClientSetupURL string `json:"client_setup_url,omitempty"`
}

// CampaignInfo is the campaign workspace the agent's working directory belongs
// to, as it appears on the wire. Name is empty for a campaign whose marker
// could not be read — Found, not Name, is the question "is the agent inside a
// campaign".
type CampaignInfo struct {
	Found bool   `json:"found"`
	Name  string `json:"name"`
	ID    string `json:"id"`
	Root  string `json:"root"`
}

// PairingCodeBanner is written to stdout whenever serve mints a pairing code
// (the Mac app renders a QR from it).
type PairingCodeBanner struct {
	Event     string `json:"event"` // always "pairing_code"
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"` // RFC3339
}
