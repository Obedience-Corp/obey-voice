package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func origin(us int64) *int64 { return &us }
func boolPtr(b bool) *bool   { return &b }

func validRequest() Request {
	return Request{
		Protocol: Protocol,
		JobID:    "take-20260918-001-attempt-1",
		Input: Input{
			Path:             "/tmp/job/audio.wav",
			Format:           FormatWAVPCMS16LE,
			SampleRateHz:     SampleRateHz,
			Channels:         Channels,
			TimelineOriginUS: origin(0),
		},
		Language: "en",
		Timing:   TimingUtterance,
	}
}

func TestStableFailureCodeSpellings(t *testing.T) {
	codes := []struct {
		got, want string
	}{
		{CodeInvalidRequest, "invalid_request"},
		{CodeUnsupportedProtocol, "unsupported_protocol"},
		{CodeUnsupportedCapability, "unsupported_capability"},
		{CodeInputUnreadable, "input_unreadable"},
		{CodeInputFormatMismatch, "input_format_mismatch"},
		{CodeProviderUnavailable, "provider_unavailable"},
		{CodeModelMissing, "model_missing"},
		{CodeDecodeFailed, "decode_failed"},
		{CodeInternal, "internal"},
	}
	for _, tc := range codes {
		if tc.got != tc.want {
			t.Errorf("code = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestErrorAsAndUnwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := Errorf(CodeInputUnreadable, false, "open wav: %w", inner)

	var coded *Error
	if !errors.As(err, &coded) {
		t.Fatalf("errors.As(%v, *Error) failed", err)
	}
	if coded.Code != CodeInputUnreadable {
		t.Errorf("code = %q, want %q", coded.Code, CodeInputUnreadable)
	}
	if coded.Unwrap() == nil {
		t.Errorf("Unwrap() = nil, want a cause")
	}
	if !errors.Is(err, inner) {
		t.Errorf("errors.Is cannot reach the cause through Unwrap: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, CodeInputUnreadable) || !strings.Contains(got, "disk full") {
		t.Errorf("Error() = %q, want the code and the cause", got)
	}
}

func TestParseRequestReadsTheSpecExample(t *testing.T) {
	const doc = `{
  "protocol": "obey-transcript/v1",
  "job_id": "take-20260918-001-attempt-1",
  "input": {
    "path": "/absolute/job/audio.wav",
    "format": "wav_pcm_s16le",
    "sample_rate_hz": 16000,
    "channels": 1,
    "timeline_origin_us": 0
  },
  "provider": "configured",
  "language": "en",
  "timing": "utterance",
  "diarization": false,
  "allow_model_download": false
}`
	req, err := ParseRequest(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if req.Input.Path != "/absolute/job/audio.wav" || req.Provider != ProviderConfigured {
		t.Fatalf("parsed %+v", req)
	}
}

func TestParseRequestIgnoresUnknownFields(t *testing.T) {
	const doc = `{"protocol":"obey-transcript/v1","job_id":"j","speaker_hints":["lance"],
	 "input":{"path":"/a.wav","format":"wav_pcm_s16le","sample_rate_hz":16000,"channels":1,"timeline_origin_us":0,"future":true},
	 "timing":"utterance"}`
	req, err := ParseRequest(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestParseRequestRejectsMalformedJSON(t *testing.T) {
	_, err := ParseRequest(strings.NewReader("{not json"))
	coded, ok := errors.AsType[*Error](err)
	if !ok || coded.Code != CodeInvalidRequest {
		t.Fatalf("ParseRequest = %v, want %s", err, CodeInvalidRequest)
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("errors.AsType cannot reach json.SyntaxError through Unwrap: %v", err)
	}
}

func TestParseRequestTrailingInput(t *testing.T) {
	const doc = `{"protocol":"obey-transcript/v1","job_id":"j","input":{"path":"/a.wav","format":"wav_pcm_s16le","sample_rate_hz":16000,"channels":1,"timeline_origin_us":0},"timing":"utterance"}`

	t.Run("trailing whitespace", func(t *testing.T) {
		req, err := ParseRequest(strings.NewReader(doc + " \n\t"))
		if err != nil {
			t.Fatalf("ParseRequest: %v", err)
		}
		if req.JobID != "j" {
			t.Fatalf("job_id = %q", req.JobID)
		}
	})

	for _, tc := range []struct {
		name, extra string
	}{
		{"concatenated document", doc},
		{"second json value", " null"},
		{"trailing non-whitespace", " extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRequest(strings.NewReader(doc + tc.extra))
			coded, ok := errors.AsType[*Error](err)
			if !ok || coded.Code != CodeInvalidRequest {
				t.Fatalf("ParseRequest = %v, want %s", err, CodeInvalidRequest)
			}
		})
	}
}

func TestParseRequestRejectsAnOversizedDocument(t *testing.T) {
	_, err := ParseRequest(strings.NewReader(strings.Repeat("x", MaxRequestBytes+1)))
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeInvalidRequest {
		t.Fatalf("ParseRequest = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error %q does not name the size limit", err)
	}
}

func TestParseRequestWrapsAReadError(t *testing.T) {
	inner := errors.New("read failed")
	_, err := ParseRequest(errReader{err: inner})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeInvalidRequest {
		t.Fatalf("ParseRequest = %v, want %s", err, CodeInvalidRequest)
	}
	if !errors.Is(err, inner) {
		t.Fatalf("errors.Is cannot reach the read cause through Unwrap: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = errReader{}

func TestValidateAppliesDefaults(t *testing.T) {
	req := validRequest()
	req.Provider = ""
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	switch {
	case req.Provider != ProviderConfigured:
		t.Errorf("provider = %q, want %q", req.Provider, ProviderConfigured)
	case req.Diarization == nil || *req.Diarization:
		t.Errorf("diarization = %v, want a false default", req.Diarization)
	case req.AllowModelDownload == nil || *req.AllowModelDownload:
		t.Errorf("allow_model_download = %v, want a false default", req.AllowModelDownload)
	}
}

func TestValidateAllowsAnOmittedLanguage(t *testing.T) {
	req := validRequest()
	req.Language = ""
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateAcceptsALanguageRegionTag(t *testing.T) {
	req := validRequest()
	req.Language = "en-US"
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if req.Language != "en-US" {
		t.Fatalf("language = %q", req.Language)
	}
}

func TestValidateAcceptsALanguageScriptRegionTag(t *testing.T) {
	req := validRequest()
	req.Language = "zh-Hant-TW"
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if req.Language != "zh-Hant-TW" {
		t.Fatalf("language = %q", req.Language)
	}
}

func TestValidateAcceptsALaterMinorVersion(t *testing.T) {
	for _, version := range []string{"obey-transcript/v1.4", "obey-transcript/v1.4.0"} {
		req := validRequest()
		req.Protocol = version
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", version, err)
		}
	}
}

func TestValidateRejections(t *testing.T) {
	mut := func(f func(*Request)) Request { r := validRequest(); f(&r); return r }

	cases := []struct {
		name string
		req  Request
		code string
		want string
	}{
		{"no protocol", mut(func(r *Request) { r.Protocol = "" }), CodeInvalidRequest, "protocol is required"},
		{"next major version", mut(func(r *Request) { r.Protocol = "obey-transcript/v2" }), CodeUnsupportedProtocol, "major version 2"},
		{"another protocol", mut(func(r *Request) { r.Protocol = "whisper-json/v1" }), CodeUnsupportedProtocol, "is not \"obey-transcript/v1\""},
		{"unversioned protocol", mut(func(r *Request) { r.Protocol = "obey-transcript" }), CodeUnsupportedProtocol, "is not \"obey-transcript/v1\""},
		{"malformed minor", mut(func(r *Request) { r.Protocol = "obey-transcript/v1.x" }), CodeUnsupportedProtocol, "has an invalid version"},
		{"trailing version dot", mut(func(r *Request) { r.Protocol = "obey-transcript/v1." }), CodeUnsupportedProtocol, "has an invalid version"},
		{"empty version component", mut(func(r *Request) { r.Protocol = "obey-transcript/v1..2" }), CodeUnsupportedProtocol, "has an invalid version"},
		{"blank job id", mut(func(r *Request) { r.JobID = "   " }), CodeInvalidRequest, "job_id is required"},
		{"no path", mut(func(r *Request) { r.Input.Path = "" }), CodeInvalidRequest, "input.path is required"},
		{"relative path", mut(func(r *Request) { r.Input.Path = "audio.wav" }), CodeInvalidRequest, "is not absolute"},
		{"no format", mut(func(r *Request) { r.Input.Format = "" }), CodeInvalidRequest, "input.format is required"},
		{"other format", mut(func(r *Request) { r.Input.Format = "flac" }), CodeUnsupportedCapability, "input.format \"flac\""},
		{"no rate", mut(func(r *Request) { r.Input.SampleRateHz = 0 }), CodeInvalidRequest, "input.sample_rate_hz is required"},
		{"other rate", mut(func(r *Request) { r.Input.SampleRateHz = 48000 }), CodeUnsupportedCapability, "48000"},
		{"no channels", mut(func(r *Request) { r.Input.Channels = 0 }), CodeInvalidRequest, "input.channels is required"},
		{"stereo", mut(func(r *Request) { r.Input.Channels = 2 }), CodeUnsupportedCapability, "input.channels 2"},
		{"no origin", mut(func(r *Request) { r.Input.TimelineOriginUS = nil }), CodeInvalidRequest, "input.timeline_origin_us is required"},
		{"nonzero origin", mut(func(r *Request) { r.Input.TimelineOriginUS = origin(500) }), CodeUnsupportedCapability, "requires 0"},
		{"no timing", mut(func(r *Request) { r.Timing = "" }), CodeInvalidRequest, "timing is required"},
		{"word timing", mut(func(r *Request) { r.Timing = TimingWord }), CodeUnsupportedCapability, "not implemented"},
		{"timing none", mut(func(r *Request) { r.Timing = TimingNone }), CodeUnsupportedCapability, "cannot produce captions"},
		{"unknown timing", mut(func(r *Request) { r.Timing = "sentence" }), CodeInvalidRequest, "not a known value"},
		{"named provider", mut(func(r *Request) { r.Provider = "parakeet" }), CodeUnsupportedCapability, "cannot be selected per request"},
		{"diarization on", mut(func(r *Request) { r.Diarization = boolPtr(true) }), CodeUnsupportedCapability, "diarization"},
		{"downloads on", mut(func(r *Request) { r.AllowModelDownload = boolPtr(true) }), CodeUnsupportedCapability, "models ensure"},
		{"junk language", mut(func(r *Request) { r.Language = "en_US;rm -rf" }), CodeInvalidRequest, "language tag"},
		{"consecutive language hyphens", mut(func(r *Request) { r.Language = "en--US" }), CodeInvalidRequest, "language tag"},
		{"empty language subtag", mut(func(r *Request) { r.Language = "en-" }), CodeInvalidRequest, "language tag"},
		{"leading language hyphen", mut(func(r *Request) { r.Language = "-en" }), CodeInvalidRequest, "language tag"},
		{"numeric language", mut(func(r *Request) { r.Language = "123" }), CodeInvalidRequest, "language tag"},
		{"digit language", mut(func(r *Request) { r.Language = "1" }), CodeInvalidRequest, "language tag"},
		{"one-letter language", mut(func(r *Request) { r.Language = "e" }), CodeInvalidRequest, "language tag"},
		{"alphanumeric language", mut(func(r *Request) { r.Language = "en1" }), CodeInvalidRequest, "language tag"},
		{"overlong language primary", mut(func(r *Request) { r.Language = "abcdefghi" }), CodeInvalidRequest, "language tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			err := req.Validate()
			coded, ok := errors.AsType[*Error](err)
			if !ok {
				t.Fatalf("Validate = %v, want an *Error", err)
			}
			if coded.Code != tc.code {
				t.Errorf("code = %s, want %s (%v)", coded.Code, tc.code, err)
			}
			if coded.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if coded.Unwrap() == nil {
				t.Errorf("Unwrap() = nil, want a cause")
			}
		})
	}
}

// TestValidateChecksTheVersionBeforeAnythingElse pins the D005 order: a
// request that is wrong in several ways reports the protocol first, because a
// caller speaking another version cannot act on a field-level complaint.
func TestValidateChecksTheVersionBeforeAnythingElse(t *testing.T) {
	req := Request{Protocol: "obey-transcript/v9"}
	err := req.Validate()
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeUnsupportedProtocol {
		t.Fatalf("Validate = %v, want %s", err, CodeUnsupportedProtocol)
	}
}

// TestValidateChecksEnumsBeforeThePath pins the rest of the order: a caller
// gets the capability answer even when the path is also wrong.
func TestValidateChecksEnumsBeforeThePath(t *testing.T) {
	req := validRequest()
	req.Input.Path = "relative.wav"
	req.Timing = TimingWord
	err := req.Validate()
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != CodeUnsupportedCapability {
		t.Fatalf("Validate = %v, want %s", err, CodeUnsupportedCapability)
	}
}
