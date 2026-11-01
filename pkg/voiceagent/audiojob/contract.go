// Package audiojob defines finite, isolated generated-audio jobs. It has no
// dependency on capture, persona, conversation or playback initialization.
package audiojob

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

const Protocol = "obey-audio-job/v1"
const TextProtocol = "obey-audio-job/v2"
const DubbingProtocol = "obey-audio-job/v3"
const ConversionProtocol = "obey-audio-job/v4"
const ConfigSchema = "obey-voice.audio-config/v1"

type Source struct {
	MediaSHA256     string `json:"media_sha256"`
	ClipID          string `json:"clip_id"`
	RangeID         string `json:"range_id"`
	ProjectRevision string `json:"project_revision"`
}
type Input struct {
	// Original source stream identity; PCM decoding still produces one mono WAV.
	StreamIndex         uint32 `json:"stream_index,omitempty"`
	Path                string `json:"path"`
	SHA256              string `json:"sha256"`
	SampleCount         int64  `json:"sample_count"`
	SampleRate          int    `json:"sample_rate_hz"`
	Channels            int    `json:"channels"`
	ChannelRole         string `json:"channel_role"`
	SourceOriginSamples int64  `json:"source_origin_samples"`
}
type Runtime struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	Voice    string `json:"voice"`
}

// Origin is the canonical outbound server origin. Validation admits an optional
// root slash; retained request/config values stay exact for provenance matching.
func (r Runtime) Origin() string {
	endpoint, err := url.Parse(r.Endpoint)
	if err != nil {
		return ""
	} // Config.Validate rejects malformed endpoints first.
	return (&url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host}).String()
}

type Limits struct {
	TimeoutSeconds int   `json:"timeout_seconds"`
	OutputBytes    int64 `json:"output_bytes"`
}
type Request struct {
	Protocol          string             `json:"protocol"`
	JobID             string             `json:"job_id"`
	Task              string             `json:"task"`
	Source            Source             `json:"source"`
	Input             Input              `json:"input"`
	Runtime           Runtime            `json:"runtime"`
	Script            string             `json:"script"`
	Instruction       string             `json:"instruction,omitempty"`
	Language          string             `json:"language"`
	TargetLanguage    string             `json:"target_language,omitempty"`
	DurationPolicy    string             `json:"duration_policy"`
	OutputDir         string             `json:"output_dir"`
	Limits            Limits             `json:"limits"`
	TranslationReview *TranslationReview `json:"translation_review,omitempty"`
	Reference         *Input             `json:"reference,omitempty"`
}

// Config is deliberately independent of the assistant config and credentials.
type Config struct {
	Schema      string          `json:"schema"`
	Runtime     Runtime         `json:"runtime"`
	StorageRoot string          `json:"storage_root"`
	Managed     *ManagedConfig  `json:"managed,omitempty"`
	Waveform    *WaveformConfig `json:"waveform,omitempty"`
}
type Window struct {
	StartSample int64 `json:"start_sample"`
	EndSample   int64 `json:"end_sample"`
}
type Output struct {
	Kind        string `json:"kind,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	SampleRate  int    `json:"sample_rate_hz,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	SampleCount int64  `json:"sample_count,omitempty"`
}
type Manifest struct {
	Protocol                  string              `json:"protocol"`
	JobID                     string              `json:"job_id"`
	Task                      string              `json:"task"`
	Source                    Source              `json:"source"`
	Input                     Input               `json:"input"`
	Runtime                   Runtime             `json:"runtime"`
	RuntimeVersion            string              `json:"runtime_version"`
	ModelDigest               string              `json:"model_digest"`
	Transport                 string              `json:"transport"`
	ScriptSHA256              string              `json:"script_sha256"`
	Language                  string              `json:"language"`
	TargetLanguage            string              `json:"target_language,omitempty"`
	DurationPolicy            string              `json:"duration_policy"`
	SourceWindows             []Window            `json:"source_windows"`
	Output                    Output              `json:"output"`
	ProjectorDigest           string              `json:"projector_digest,omitempty"`
	RuntimeDigest             string              `json:"runtime_digest,omitempty"`
	Sampling                  *Sampling           `json:"sampling,omitempty"`
	InstructionSHA256         string              `json:"instruction_sha256,omitempty"`
	PromptSHA256              string              `json:"prompt_sha256,omitempty"`
	ChunkPolicy               string              `json:"chunk_policy,omitempty"`
	TextSampling              *TextSampling       `json:"text_sampling,omitempty"`
	TranslationReview         *TranslationReview  `json:"translation_review,omitempty"`
	TranslationManifestSHA256 string              `json:"translation_manifest_sha256,omitempty"`
	ApprovedScript            string              `json:"approved_script,omitempty"`
	Reference                 *Input              `json:"reference,omitempty"`
	EncoderDigest             string              `json:"encoder_digest,omitempty"`
	WorkerDigest              string              `json:"worker_digest,omitempty"`
	ConversionSampling        *ConversionSampling `json:"conversion_sampling,omitempty"`
}

// Capability distinguishes transport support and installed assets from a task's
// independent quality qualification. Ready is never inferred from model names.
type Capability struct {
	Task            string   `json:"task"`
	Supported       bool     `json:"supported"`
	Installed       bool     `json:"installed"`
	Qualified       bool     `json:"qualified"`
	Reason          string   `json:"reason"`
	Protocol        string   `json:"protocol,omitempty"`
	Languages       []string `json:"languages,omitempty"`
	TargetLanguages []string `json:"target_languages,omitempty"`
	Voices          []string `json:"voices,omitempty"`
	OutputFormat    string   `json:"output_format,omitempty"`
}

func Decode(r io.Reader, out any) error {
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("audio job document exceeds 1 MiB")
	}
	return json.Unmarshal(b, out)
}
func HashValid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (c Config) Validate() error {
	if c.Schema != ConfigSchema || !filepath.IsAbs(c.StorageRoot) {
		return fmt.Errorf("audio config requires %s and absolute storage_root", ConfigSchema)
	}
	if c.Runtime.Provider == "openvoice" {
		if c.Waveform == nil || c.Managed != nil || c.Runtime.Endpoint != "" || c.Runtime.Model != "openvoice-v2" || c.Runtime.Voice != "reference" {
			return fmt.Errorf("waveform conversion requires explicit openvoice-v2 pack and reference voice, without endpoint")
		}
		return c.Waveform.Validate()
	}
	if c.Waveform != nil {
		return fmt.Errorf("waveform pack requires openvoice runtime")
	}
	if c.Runtime.Provider == "llama.cpp" {
		if c.Managed == nil || c.Runtime.Endpoint != "" || c.Runtime.Model != ManagedModel || (c.Runtime.Voice != "female" && c.Runtime.Voice != "male") {
			return fmt.Errorf("managed audio requires an explicit llama.cpp pack, empty endpoint, lfm3-audio-2b model and female or male voice")
		}
		return c.Managed.Validate()
	}
	if c.Managed != nil {
		return fmt.Errorf("managed config cannot select an external runtime")
	}
	if c.Runtime.Provider != "ollama" || strings.TrimSpace(c.Runtime.Model) == "" {
		return fmt.Errorf("audio config requires explicit ollama model")
	}
	u, err := url.Parse(c.Runtime.Endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("local endpoint must be an HTTP loopback origin without credentials")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("local endpoint must be loopback")
	}
	return nil
}
func (r Request) Validate(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if (r.Protocol != Protocol && r.Protocol != TextProtocol && r.Protocol != DubbingProtocol && r.Protocol != ConversionProtocol) || r.JobID == "" || strings.ContainsAny(r.JobID, "/\\\x00") || r.JobID == "." || r.JobID == ".." {
		return fmt.Errorf("invalid protocol or job_id")
	}
	if r.Runtime != c.Runtime {
		return fmt.Errorf("request runtime must match isolated audio config")
	}
	if !HashValid(r.Source.MediaSHA256) || r.Source.ClipID == "" || r.Source.RangeID == "" || r.Source.ProjectRevision == "" {
		return fmt.Errorf("missing source identity")
	}
	rate := 16000
	if r.Protocol == ConversionProtocol {
		rate = 22050
	}
	if !filepath.IsAbs(r.Input.Path) || !HashValid(r.Input.SHA256) || r.Input.SampleCount <= 0 || r.Input.SampleRate != rate || r.Input.Channels != 1 || r.Input.SourceOriginSamples < 0 || r.Input.SourceOriginSamples > math.MaxInt64-r.Input.SampleCount {
		return fmt.Errorf("input requires nonempty hash-addressed task-rate mono WAV and source origin")
	}
	switch r.Input.ChannelRole {
	case "microphone", "speech", "mixed":
	default:
		return fmt.Errorf("unknown input channel role")
	}
	if r.Protocol != DubbingProtocol && r.TranslationReview != nil {
		return fmt.Errorf("translation review is only valid for explicit v3 dubbing")
	}
	if r.Protocol != ConversionProtocol && (r.Reference != nil || c.Waveform != nil) {
		return fmt.Errorf("waveform pack/reference only valid for explicit v4 conversion")
	}
	if r.Protocol == ConversionProtocol {
		if err := r.validateConversion(c); err != nil {
			return err
		}
	} else if r.Protocol == DubbingProtocol {
		if err := r.validateDubbing(c); err != nil {
			return err
		}
	} else if r.Protocol == TextProtocol {
		if err := r.validateTextTask(c); err != nil {
			return err
		}
	} else {
		if r.Task != "narration" {
			return fmt.Errorf("task %q is unavailable in v1; conversion, dubbing and alignment require independent qualification", r.Task)
		}
		if strings.TrimSpace(r.Script) == "" || len(r.Script) > 256<<10 || r.Language != "en" || r.TargetLanguage != "" || r.Instruction != "" {
			return fmt.Errorf("narration requires an approved English script of at most 256 KiB and no separate instruction")
		}
		if c.Managed != nil && len(r.Script) > 1024 {
			return fmt.Errorf("managed narration accepts at most 1024 UTF-8 bytes; select a shorter script")
		}
	}
	if r.DurationPolicy != "preserve_picture" {
		return fmt.Errorf("only explicit preserve_picture duration policy is supported")
	}
	if r.Limits.TimeoutSeconds < 1 || r.Limits.TimeoutSeconds > 3600 || r.Limits.OutputBytes < 44 || r.Limits.OutputBytes > 1<<30 {
		return fmt.Errorf("invalid resource limits")
	}
	rel, err := filepath.Rel(c.StorageRoot, r.OutputDir)
	if err != nil || !filepath.IsAbs(r.OutputDir) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("output_dir must be beneath isolated storage_root")
	}
	return nil
}
