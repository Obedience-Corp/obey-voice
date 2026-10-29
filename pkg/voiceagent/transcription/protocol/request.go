package protocol

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// Protocol is the wire contract this package implements.
	Protocol = "obey-transcript/v1"
	// ProtocolName and ProtocolMajor are Protocol split at the slash. A
	// request may name a higher minor version (obey-transcript/v1.2): within a
	// major version, fields are only ever added.
	ProtocolName  = "obey-transcript"
	ProtocolMajor = 1

	// FormatWAVPCMS16LE is the only input encoding v1 accepts.
	FormatWAVPCMS16LE = "wav_pcm_s16le"
	// SampleRateHz and Channels are the only input geometry v1 accepts.
	SampleRateHz = 16000
	Channels     = 1

	// ProviderConfigured is the only provider value v1 accepts: the backend is
	// whatever stt_provider selects.
	ProviderConfigured = "configured"

	// Timing granularities named by the spec. Only TimingUtterance is
	// implemented.
	TimingUtterance = "utterance"
	TimingWord      = "word"
	TimingNone      = "none"

	// MaxRequestBytes bounds a request document so a wrong path cannot make the
	// command read an arbitrary file into memory.
	MaxRequestBytes = 1 << 20
)

// Input describes the audio file to transcribe.
type Input struct {
	Path         string `json:"path"`
	Format       string `json:"format"`
	SampleRateHz int    `json:"sample_rate_hz"`
	Channels     int    `json:"channels"`
	// TimelineOriginUS is required and must be 0 in v1. The pointer is what
	// tells an omitted field from an explicit zero.
	TimelineOriginUS *int64 `json:"timeline_origin_us"`
}

// Request is the obey-transcript/v1 job document.
type Request struct {
	Protocol string `json:"protocol"`
	JobID    string `json:"job_id"`
	Input    Input  `json:"input"`
	Provider string `json:"provider,omitempty"`
	Language string `json:"language,omitempty"`
	Timing   string `json:"timing"`
	// Diarization and AllowModelDownload default to false when omitted.
	Diarization        *bool `json:"diarization,omitempty"`
	AllowModelDownload *bool `json:"allow_model_download,omitempty"`
}

// ParseRequest decodes a request document. Unknown fields are ignored: within
// a major version new fields are additive, and a client that sends one built
// for a later build must still work here.
func ParseRequest(r io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxRequestBytes+1))
	if err != nil {
		return Request{}, Errorf(CodeInvalidRequest, false, "read request: %w", err)
	}
	if len(data) > MaxRequestBytes {
		return Request{}, Errorf(CodeInvalidRequest, false, "request is larger than %d bytes", MaxRequestBytes)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, Errorf(CodeInvalidRequest, false, "parse request json: %w", err)
	}
	return req, nil
}

// Validate applies the v1 defaults and checks the request in the order the
// contract fixes: protocol version, required fields, enum values, then the
// input path. Every failure it returns is an *Error whose code exits 2.
//
// It takes a pointer because applying a default is part of validating: after
// it returns nil, Provider, Diarization and AllowModelDownload are set.
func (r *Request) Validate() error {
	if strings.TrimSpace(r.Protocol) == "" {
		return required("protocol")
	}
	if err := checkProtocolVersion(r.Protocol); err != nil {
		return err
	}

	r.JobID = strings.TrimSpace(r.JobID)
	if r.JobID == "" {
		return required("job_id")
	}
	if strings.TrimSpace(r.Input.Path) == "" {
		return required("input.path")
	}
	if strings.TrimSpace(r.Input.Format) == "" {
		return required("input.format")
	}
	if r.Input.SampleRateHz == 0 {
		return required("input.sample_rate_hz")
	}
	if r.Input.Channels == 0 {
		return required("input.channels")
	}
	if r.Input.TimelineOriginUS == nil {
		return required("input.timeline_origin_us")
	}
	if strings.TrimSpace(r.Timing) == "" {
		return required("timing")
	}

	// Enum values. A named value this build cannot honor is an unsupported
	// capability; a value that names nothing is a malformed request. Both exit
	// 2, and the distinction is what a caller reads to decide whether to change
	// the request or the installation.
	if r.Input.Format != FormatWAVPCMS16LE {
		return Errorf(CodeUnsupportedCapability, false,
			"input.format %q is not supported; v1 accepts %q", r.Input.Format, FormatWAVPCMS16LE)
	}
	if r.Input.SampleRateHz != SampleRateHz {
		return Errorf(CodeUnsupportedCapability, false,
			"input.sample_rate_hz %d is not supported; v1 accepts %d", r.Input.SampleRateHz, SampleRateHz)
	}
	if r.Input.Channels != Channels {
		return Errorf(CodeUnsupportedCapability, false,
			"input.channels %d is not supported; v1 accepts %d", r.Input.Channels, Channels)
	}
	if origin := *r.Input.TimelineOriginUS; origin != 0 {
		return Errorf(CodeUnsupportedCapability, false,
			"input.timeline_origin_us is %d; v1 requires 0, so normalize the file to its playback timeline before submitting", origin)
	}

	switch r.Timing {
	case TimingUtterance:
	case TimingWord:
		return Errorf(CodeUnsupportedCapability, false,
			"timing %q is not implemented by any provider in this build", TimingWord)
	case TimingNone:
		return Errorf(CodeUnsupportedCapability, false,
			"timing %q cannot produce captions; v1 requires %q", TimingNone, TimingUtterance)
	default:
		return Errorf(CodeInvalidRequest, false,
			"timing %q is not a known value; v1 accepts %q", r.Timing, TimingUtterance)
	}

	switch provider := strings.TrimSpace(r.Provider); provider {
	case "":
		r.Provider = ProviderConfigured
	case ProviderConfigured:
		r.Provider = ProviderConfigured
	default:
		return Errorf(CodeUnsupportedCapability, false,
			"provider %q cannot be selected per request; v1 accepts %q and uses the stt_provider setting", provider, ProviderConfigured)
	}

	if r.Language = strings.TrimSpace(r.Language); r.Language != "" && !languageTag(r.Language) {
		return Errorf(CodeInvalidRequest, false,
			"language %q is not a language tag such as \"en\" or \"en-US\"", r.Language)
	}

	if r.Diarization == nil {
		r.Diarization = new(bool)
	} else if *r.Diarization {
		return Errorf(CodeUnsupportedCapability, false,
			"diarization is not available in v1")
	}
	if r.AllowModelDownload == nil {
		r.AllowModelDownload = new(bool)
	} else if *r.AllowModelDownload {
		return Errorf(CodeUnsupportedCapability, false,
			"allow_model_download is not available in v1; install models with `obey-voice models ensure`")
	}

	if !filepath.IsAbs(r.Input.Path) {
		return Errorf(CodeInvalidRequest, false, "input.path %q is not absolute", r.Input.Path)
	}
	return nil
}

func required(field string) *Error {
	return Errorf(CodeInvalidRequest, false, "%s is required", field)
}

// checkProtocolVersion accepts this major version and rejects every other
// protocol string, including one that is not this protocol at all.
func checkProtocolVersion(s string) error {
	name, version, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || name != ProtocolName || !strings.HasPrefix(version, "v") {
		return Errorf(CodeUnsupportedProtocol, false, "protocol %q is not %q", s, Protocol)
	}
	major, err := parseProtocolMajor(version[len("v"):])
	if err != nil {
		return Errorf(CodeUnsupportedProtocol, false, "protocol %q has an invalid version", s)
	}
	if major != ProtocolMajor {
		return Errorf(CodeUnsupportedProtocol, false,
			"protocol major version %d is not supported; this build speaks %q", major, Protocol)
	}
	return nil
}

// parseProtocolMajor reads MAJOR or MAJOR.MINOR[.PATCH]... after the leading
// "v". Every component must be a canonical non-negative integer: no empty
// parts, no extra dots, no leading zeros except the number 0.
func parseProtocolMajor(rest string) (int, error) {
	parts := strings.Split(rest, ".")
	if len(parts) == 0 {
		return 0, strconv.ErrSyntax
	}
	major, err := parseVersionComponent(parts[0])
	if err != nil {
		return 0, err
	}
	for _, part := range parts[1:] {
		if _, err := parseVersionComponent(part); err != nil {
			return 0, err
		}
	}
	return major, nil
}

func parseVersionComponent(s string) (int, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

// languageTag reports whether s looks like a BCP 47 tag this command could
// pass to a provider. It is a shape check, not a registry lookup: the primary
// subtag is 2-8 ASCII letters and each later subtag is 1-8 ASCII
// alphanumeric characters. Whether the resolved model transcribes that
// language is answered by the caller.
func languageTag(s string) bool {
	if len(s) > 35 {
		return false
	}
	first := true
	for part := range strings.SplitSeq(s, "-") {
		if first {
			if len(part) < 2 || len(part) > 8 {
				return false
			}
		} else if part == "" || len(part) > 8 {
			return false
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			case !first && r >= '0' && r <= '9':
			default:
				return false
			}
		}
		first = false
	}
	return !first
}
