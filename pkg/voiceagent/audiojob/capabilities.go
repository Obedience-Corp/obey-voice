package audiojob

// Capabilities are collected only from the already verified owned runtime.
// Metadata describes adapter/installation support; it is not a quality pass.
type Capabilities struct {
	Schema           string       `json:"schema"`
	Runtime          Runtime      `json:"runtime"`
	RuntimeVersion   string       `json:"runtime_version"`
	ModelDigest      string       `json:"model_digest"`
	ProjectorDigest  string       `json:"projector_digest"`
	RuntimeDigest    string       `json:"runtime_digest"`
	InputFormat      string       `json:"input_format"`
	SampleRate       int          `json:"sample_rate_hz"`
	Channels         int          `json:"channels"`
	ScriptBytes      int          `json:"script_bytes"`
	InstructionBytes int          `json:"instruction_bytes"`
	ChunkPolicy      string       `json:"chunk_policy"`
	Tasks            []Capability `json:"tasks"`
}

func (p *ManagedRuntime) Capabilities(runtime Runtime, provenance Provenance) Capabilities {
	supported := p.audioInput && p.textOutput
	reason := "Preview only; independent task quality is not qualified."
	textReason := reason
	if !supported {
		textReason = "Selected runtime does not report both audio input and text output."
	}
	return Capabilities{Schema: "obey-audio-capabilities/v1", Runtime: runtime, RuntimeVersion: provenance.RuntimeVersion, ModelDigest: provenance.ModelDigest, ProjectorDigest: provenance.ProjectorDigest, RuntimeDigest: provenance.RuntimeDigest, InputFormat: "wav_pcm_s16le", SampleRate: 16000, Channels: 1, ScriptBytes: 1024, InstructionBytes: 1024, ChunkPolicy: ChunkPolicy, Tasks: []Capability{
		{Task: "narration", Supported: true, Installed: true, Qualified: false, Reason: reason, Protocol: Protocol, Languages: []string{"en"}, Voices: []string{"female", "male"}, OutputFormat: "wav_pcm_s16le_24000_mono"},
		{Task: "transcription", Supported: supported, Installed: true, Qualified: false, Reason: textReason, Protocol: TextProtocol, Languages: []string{"en"}, OutputFormat: FindingsSchema},
		{Task: "understanding", Supported: supported, Installed: true, Qualified: false, Reason: textReason, Protocol: TextProtocol, Languages: []string{"en"}, OutputFormat: FindingsSchema},
		{Task: "translation", Supported: supported, Installed: true, Qualified: false, Reason: textReason, Protocol: TextProtocol, Languages: []string{"en", "es"}, TargetLanguages: []string{"en", "es"}, OutputFormat: FindingsSchema},
		{Task: "alignment", Installed: true, Reason: "Measured word alignment is unavailable; window coverage is not word timing."},
		{Task: "voice_conversion", Installed: true, Reason: "Recording-preserving voice conversion is not qualified; no task substitution is allowed."},
		{Task: "dubbing", Supported: supported, Installed: true, Qualified: false, Reason: "Reviewed translation and duration review required; quality is not qualified.", Protocol: DubbingProtocol, Languages: []string{"en", "es"}, TargetLanguages: []string{"en", "es"}, Voices: []string{"female", "male"}, OutputFormat: "wav_pcm_s16le_24000_mono"},
	}}
}
