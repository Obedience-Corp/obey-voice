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
const ConfigSchema = "obey-voice.audio-config/v1"

type Source struct {
	MediaSHA256     string `json:"media_sha256"`
	ClipID          string `json:"clip_id"`
	RangeID         string `json:"range_id"`
	ProjectRevision string `json:"project_revision"`
}
type Input struct {
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
	Protocol       string  `json:"protocol"`
	JobID          string  `json:"job_id"`
	Task           string  `json:"task"`
	Source         Source  `json:"source"`
	Input          Input   `json:"input"`
	Runtime        Runtime `json:"runtime"`
	Script         string  `json:"script"`
	Language       string  `json:"language"`
	TargetLanguage string  `json:"target_language,omitempty"`
	DurationPolicy string  `json:"duration_policy"`
	OutputDir      string  `json:"output_dir"`
	Limits         Limits  `json:"limits"`
}

// Config is deliberately independent of the assistant config and credentials.
type Config struct {
	Schema      string         `json:"schema"`
	Runtime     Runtime        `json:"runtime"`
	StorageRoot string         `json:"storage_root"`
	Managed     *ManagedConfig `json:"managed,omitempty"`
}
type Window struct {
	StartSample int64 `json:"start_sample"`
	EndSample   int64 `json:"end_sample"`
}
type Output struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	SampleRate  int    `json:"sample_rate_hz"`
	Channels    int    `json:"channels"`
	SampleCount int64  `json:"sample_count"`
}
type Manifest struct {
	Protocol        string    `json:"protocol"`
	JobID           string    `json:"job_id"`
	Task            string    `json:"task"`
	Source          Source    `json:"source"`
	Input           Input     `json:"input"`
	Runtime         Runtime   `json:"runtime"`
	RuntimeVersion  string    `json:"runtime_version"`
	ModelDigest     string    `json:"model_digest"`
	Transport       string    `json:"transport"`
	ScriptSHA256    string    `json:"script_sha256"`
	Language        string    `json:"language"`
	DurationPolicy  string    `json:"duration_policy"`
	SourceWindows   []Window  `json:"source_windows"`
	Output          Output    `json:"output"`
	ProjectorDigest string    `json:"projector_digest,omitempty"`
	RuntimeDigest   string    `json:"runtime_digest,omitempty"`
	Sampling        *Sampling `json:"sampling,omitempty"`
}

// Capability distinguishes transport support and installed assets from a task's
// independent quality qualification. Ready is never inferred from model names.
type Capability struct {
	Task      string `json:"task"`
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Qualified bool   `json:"qualified"`
	Reason    string `json:"reason"`
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
	if r.Protocol != Protocol || r.JobID == "" || strings.ContainsAny(r.JobID, "/\\\x00") || r.JobID == "." || r.JobID == ".." {
		return fmt.Errorf("invalid protocol or job_id")
	}
	if r.Runtime != c.Runtime {
		return fmt.Errorf("request runtime must match isolated audio config")
	}
	if !HashValid(r.Source.MediaSHA256) || r.Source.ClipID == "" || r.Source.RangeID == "" || r.Source.ProjectRevision == "" {
		return fmt.Errorf("missing source identity")
	}
	if !filepath.IsAbs(r.Input.Path) || !HashValid(r.Input.SHA256) || r.Input.SampleCount <= 0 || r.Input.SampleRate != 16000 || r.Input.Channels != 1 || r.Input.SourceOriginSamples < 0 || r.Input.SourceOriginSamples > math.MaxInt64-r.Input.SampleCount {
		return fmt.Errorf("input requires nonempty hash-addressed 16 kHz mono WAV and source origin")
	}
	switch r.Input.ChannelRole {
	case "microphone", "speech", "mixed":
	default:
		return fmt.Errorf("unknown input channel role")
	}
	if r.Task != "narration" {
		return fmt.Errorf("task %q is unavailable: conversion, dubbing and alignment require independent runtime and quality qualification", r.Task)
	}
	if strings.TrimSpace(r.Script) == "" || len(r.Script) > 256<<10 || r.Language != "en" || r.TargetLanguage != "" {
		return fmt.Errorf("narration requires an approved English script of at most 256 KiB")
	}
	if c.Managed != nil && len(r.Script) > 1024 {
		return fmt.Errorf("managed narration accepts at most 1024 UTF-8 bytes; select a shorter script")
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
