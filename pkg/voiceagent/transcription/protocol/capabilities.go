package protocol

// CapabilitiesSchema identifies the `transcribe capabilities --json`
// document. A renamed or removed key bumps the version; an added key does not.
const CapabilitiesSchema = "obey-voice.transcribe-capabilities.v1"

// Capabilities is what a caller reads before it builds a request: which
// protocol versions and formats are accepted, which provider is selected,
// which models are installed, and whether anything must be installed first.
type Capabilities struct {
	Schema           string        `json:"schema"`
	Protocols        []string      `json:"protocols"`
	Formats          []Format      `json:"formats"`
	Timing           []string      `json:"timing"`
	Providers        []ProviderCap `json:"providers"`
	Readiness        Readiness     `json:"readiness"`
	DownloadRequired bool          `json:"download_required"`
	Diagnostics      []Diagnostic  `json:"diagnostics"`
}

// Format is one accepted input encoding and geometry.
type Format struct {
	Format       string `json:"format"`
	SampleRateHz int    `json:"sample_rate_hz"`
	Channels     int    `json:"channels"`
}

// ModelCap is one model belonging to a provider, and its install state.
type ModelCap struct {
	ID        string   `json:"id"`
	Revision  string   `json:"revision,omitempty"`
	Installed bool     `json:"installed"`
	Missing   []string `json:"missing,omitempty"`
}

// ProviderCap describes one speech backend this build can select.
//
// Languages lists the codes the resolved model is known to transcribe. An
// empty list with AutoDetect true means the model accepts more languages than
// this build enumerates, and a request's language is passed through.
type ProviderCap struct {
	Name          string     `json:"name"`
	Mode          string     `json:"mode,omitempty"`
	Selected      bool       `json:"selected"`
	SegmentDecode bool       `json:"segment_decode"`
	Timing        []string   `json:"timing"`
	Languages     []string   `json:"languages"`
	AutoDetect    bool       `json:"auto_detect"`
	Models        []ModelCap `json:"models"`
}

// Readiness answers "can this machine run a job right now".
type Readiness struct {
	Ready bool `json:"ready"`
	// Reason names the first thing in the way when Ready is false.
	Reason string `json:"reason,omitempty"`
	// Missing lists every asset the selected configuration needs and does not
	// have, provider models and the VAD alike.
	Missing   []string `json:"missing,omitempty"`
	ModelsDir string   `json:"models_dir"`
}

// Diagnostic mirrors one read-only setup check. It is declared here rather
// than reused from the config package so a client can depend on the wire types
// without the rest of the runtime.
type Diagnostic struct {
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// SupportedFormats is the accepted-input table every capabilities document
// reports, and the one the request validator enforces.
func SupportedFormats() []Format {
	return []Format{{Format: FormatWAVPCMS16LE, SampleRateHz: SampleRateHz, Channels: Channels}}
}

// SupportedTiming lists the timing granularities this build implements.
func SupportedTiming() []string { return []string{TimingUtterance} }
