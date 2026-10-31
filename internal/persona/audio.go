package persona

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	AudioStandard             = "standard"
	AudioNative               = "native"
	AudioControlsStandard     = "standard"
	AudioControlsConversation = "conversation"
	AudioTranscriptAuto       = "auto"
	AudioTranscriptRequired   = "required"
	AudioTranscriptOff        = "off"
	DefaultAudioModel         = "lfm3-audio:2b"
)

// ErrNativeAudioUnavailable fences hosts that have not opted into native input
// and transport integration. Resolving a candidate is not activation.
var ErrNativeAudioUnavailable = errors.New("native audio requires an audio-aware conversation host")

// Audio is an optional persona override. Omission preserves the Standard stack;
// inactive native settings may remain saved in a Standard profile.
type Audio struct {
	Mode        string `yaml:"mode,omitempty" json:"mode"`
	Provider    string `yaml:"provider,omitempty" json:"provider"`
	Model       string `yaml:"model,omitempty" json:"model"`
	Endpoint    string `yaml:"endpoint,omitempty" json:"endpoint"`
	Voice       string `yaml:"voice,omitempty" json:"voice"`
	Controls    string `yaml:"controls,omitempty" json:"controls"`
	Transcript  string `yaml:"transcript,omitempty" json:"transcript"`
	RetainAudio bool   `yaml:"retain_audio,omitempty" json:"retain_audio"`
}

func (a *Audio) defaults() Audio {
	var out Audio
	if a != nil {
		out = *a
	}
	if out.Mode == "" {
		out.Mode = AudioStandard
	}
	if out.Provider == "" {
		out.Provider = "ollama"
	}
	if out.Model == "" {
		out.Model = DefaultAudioModel
	}
	if out.Controls == "" {
		out.Controls = AudioControlsStandard
	}
	if out.Transcript == "" {
		out.Transcript = AudioTranscriptAuto
	}
	return out
}

// Validate checks syntax without contacting a runtime or rewriting the profile.
// Inactive settings do not have to satisfy native activation policy.
func (a *Audio) Validate() error {
	v := a.defaults()
	for _, e := range []struct {
		field, value string
		allowed      []string
	}{
		{"mode", v.Mode, []string{AudioStandard, AudioNative}},
		{"provider", v.Provider, []string{"ollama"}},
		{"controls", v.Controls, []string{AudioControlsStandard, AudioControlsConversation}},
		{"transcript", v.Transcript, []string{AudioTranscriptAuto, AudioTranscriptRequired, AudioTranscriptOff}},
	} {
		valid := false
		for _, choice := range e.allowed {
			valid = valid || choice == e.value
		}
		if !valid {
			return fmt.Errorf("audio.%s %q: expected %s", e.field, e.value, strings.Join(e.allowed, " or "))
		}
	}
	if strings.TrimSpace(v.Model) == "" || strings.TrimSpace(v.Model) != v.Model || strings.IndexFunc(v.Model, unicode.IsControl) >= 0 {
		return errors.New("audio.model must be a nonempty model ID without surrounding whitespace")
	}
	if v.Endpoint != "" {
		if _, err := audioEndpoint(v.Endpoint); err != nil {
			return err
		}
	}
	if strings.TrimSpace(v.Voice) != v.Voice || strings.IndexFunc(v.Voice, unicode.IsControl) >= 0 {
		return errors.New("audio.voice must not contain surrounding whitespace or control characters")
	}
	return nil
}

func audioEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	// Do not echo raw endpoints: rejected userinfo may contain credentials.
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.HasSuffix(u.Host, ":") {
		return "", errors.New("audio.endpoint must be an HTTP(S) server origin with an optional proxy prefix, without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	for _, segment := range strings.Split(strings.TrimPrefix(u.Path, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("audio.endpoint contains an ambiguous proxy path")
		}
	}
	if strings.Contains(u.Path, "//") || strings.HasSuffix(u.Path, "/api") || strings.HasSuffix(u.Path, "/v1") {
		return "", errors.New("audio.endpoint must be a server origin or proxy prefix, without /api or /v1 suffixes")
	}
	return u.String(), nil
}

// ResolveAudioConfig prepares an isolated candidate for later activation. It
// performs no network requests, capability checks, persistence, or provider construction.
// Native activation must still pass runtime checks and the selection boundary.
func ResolveAudioConfig(base *config.Config, p *Profile) (*config.Config, Audio, error) {
	if base == nil || p == nil {
		return nil, Audio{}, errors.New("persona audio: config and profile are required")
	}
	if err := p.Audio.Validate(); err != nil {
		return nil, Audio{}, err
	}
	effective := p.Audio.defaults()
	snap := base.Clone()
	Apply(snap, p)
	if effective.Mode == AudioStandard {
		return snap, effective, nil
	}
	if effective.Controls == AudioControlsStandard && effective.Transcript == AudioTranscriptOff {
		return nil, Audio{}, errors.New("native Standard controls require transcription; choose auto or required")
	}
	if snap.AcousticNameEnabled {
		return nil, Audio{}, errors.New("native audio is incompatible with acoustic name recognition; disable it separately or select Standard mode")
	}
	if effective.Controls == AudioControlsConversation && snap.GroupParticipationEnabled {
		return nil, Audio{}, errors.New("native conversation controls are incompatible with group participation")
	}
	endpoint := effective.Endpoint
	if endpoint == "" {
		endpoint = snap.OllamaHost
	}
	var err error
	effective.Endpoint, err = audioEndpoint(endpoint)
	if err != nil {
		return nil, Audio{}, err
	}
	// Freeze inherited worker routing BEFORE changing the live model or origin.
	// Tiered assignments remain independent and retain their cloned policy.
	if snap.VoiceWorkers == nil || snap.VoiceWorkers.Mode != config.WorkerModeTiered {
		worker := config.WorkerBrainConfig(snap)
		snap.VoiceWorkerProvider = worker.BrainProvider
		snap.VoiceWorkerModel = ModelForProvider(worker, worker.BrainProvider)
		if worker.BrainProvider == "ollama" {
			snap.VoiceWorkerOllamaHost = worker.OllamaHost
		}
	}
	snap.BrainProvider = effective.Provider
	snap.OllamaModel = effective.Model
	snap.OllamaHost = effective.Endpoint
	snap.TTSProvider = config.OpenAISpeechProviderName
	snap.OpenAISpeechRuntime = "external"
	snap.OpenAISpeechExternal = true
	snap.OpenAISpeechURL = effective.Endpoint + "/v1"
	snap.OpenAISpeechModel = effective.Model
	snap.OpenAISpeechVoice = effective.Voice // blank means runtime default
	// The current Ollama brain uses no configurable bearer credentials. Never
	// borrow credentials or placement from a different speech provider.
	snap.OpenAISpeechAPIKey = ""
	snap.OpenAISpeechBackend = ""
	snap.TTSPlace = config.TTSPlaceLocal
	snap.TTSWorkerURL, snap.TTSWorkerToken = "", ""
	snap.TTSFallbackProvider = "disabled"
	snap.TTSLookahead = 1
	return snap, effective, nil
}
