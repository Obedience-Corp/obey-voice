package qwen

import (
	"context"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"strings"

	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// ListVoices reports speakers advertised by the managed/native handshake.
// The external CLI contract has no verified voice discovery mechanism.
func (q *Qwen3TTS) ListVoices(locale, gender string) []tts.Voice {
	if !q.managed && !q.native {
		return nil
	}
	var voices []tts.Voice
	for _, mode := range q.Capabilities().Modes {
		if mode.ID != tts.VoiceModeCustomVoice {
			continue
		}
		for _, voice := range mode.Voices {
			if locale != "" && !strings.EqualFold(locale, voice.Locale) {
				continue
			}
			if gender != "" && !strings.EqualFold(gender, voice.Gender) {
				continue
			}
			voices = append(voices, voice)
		}
	}
	return voices
}

// Capabilities reports the managed/native worker handshake or the baseline
// guarantees of the external CLI adapter.
func (q *Qwen3TTS) Capabilities() tts.ProviderCapabilities {
	caps := tts.ProviderCapabilities{
		Provider:               qwen3TTSProviderName,
		Model:                  q.model,
		ModelReady:             q.model != "" && q.Available(),
		SampleRates:            []int{qwen3TTSSampleRate},
		SupportsCancellation:   true,
		SupportsReferenceAudio: q.native && !q.remote, // native stage A accepts ref_wav; the LAN wire does not carry it
		SupportsSpeed:          false,
		SupportsVocalTags:      false,
	}
	if !q.managed && !q.native {
		return caps
	}
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()
	registry := managedqwen.SelectableVoices()
	voiceNames := make([]string, 0, len(registry))
	switch {
	case q.native && q.speech != nil && len(q.speech.Presets()) > 0:
		voiceNames = append(voiceNames, q.speech.Presets()...)
	case q.managed && q.session != nil && len(q.session.voices) > 0:
		voiceNames = append(voiceNames, q.session.voices...)
	case q.native && !q.remote:
		if names := loadNativePresetsFromDisk(q.model); len(names) > 0 {
			voiceNames = names
		} else {
			for _, voice := range registry {
				voiceNames = append(voiceNames, voice.Name)
			}
		}
	default:
		for _, voice := range registry {
			voiceNames = append(voiceNames, voice.Name)
		}
	}
	if !q.remote {
		// A shipped clone rides a ref_wav, which the LAN wire does not carry.
		// Advertising one here would promise a voice the session cannot speak.
		voiceNames = appendShippedVoiceNames(voiceNames)
	}
	voices := make([]tts.Voice, 0, len(voiceNames))
	for _, name := range voiceNames {
		voice := managedqwen.Voice{Name: name}
		for _, candidate := range registry {
			if strings.EqualFold(candidate.Name, name) {
				voice = candidate
				break
			}
		}
		friendly := voice.Description
		if friendly == "" {
			friendly = name
		}
		voices = append(voices, tts.Voice{
			Name: voice.Name, FriendlyName: friendly,
			Gender: "preset", Locale: voice.NativeLanguage,
		})
	}
	caps.Modes = []tts.VoiceModeCapability{{
		ID: tts.VoiceModeCustomVoice, Voices: voices, SupportsInstruction: false,
		RequiresReferenceAudio: false,
	}}
	if q.managed && q.session != nil && len(q.session.languages) > 0 {
		caps.Languages = append([]string(nil), q.session.languages...)
	} else {
		caps.Languages = managedqwen.SupportedLanguages()
	}
	// Stage A: streaming=false at protocol layer; PCM still arrives as one chunk post-synth.
	if q.native {
		caps.SupportsStreaming = q.WorkerStreaming()
	}
	caps.SupportsPreview = true
	return caps
}

// RewriteVocalTags strips bracket tags; this codec reads them as words.
func (q *Qwen3TTS) RewriteVocalTags(s string, pick func(kind string) string) string {
	_ = pick
	return tts.StripVocalTags(s)
}

func (q *Qwen3TTS) Status() tts.ProviderStatus {
	available := q.Available()
	detail := "external native CLI and model configured"
	if q.remote {
		detail = "speech worker " + q.remoteAddr + " connected"
	} else if q.native {
		detail = "native qwen3-tts-worker warm session ready"
	} else if q.managed {
		detail = "managed CustomVoice runtime and preset speakers ready"
	}
	if !available {
		detail = "provider is closed"
	} else if q.native {
		q.sessionMu.Lock()
		workerReady := q.speech != nil
		q.sessionMu.Unlock()
		if !workerReady {
			detail = "native worker stopped; it will restart on the next request"
			if q.remote {
				detail = "speech worker " + q.remoteAddr + " disconnected; it will redial on the next request"
			}
		}
	} else if q.managed {
		q.sessionMu.Lock()
		workerReady := q.session != nil
		q.sessionMu.Unlock()
		if !workerReady {
			detail = "managed worker stopped; it will restart on the next request"
		}
	}
	return tts.ProviderStatus{Provider: qwen3TTSProviderName, Available: available, ModelReady: q.model != "" && available, Detail: detail}
}

func bindShippedOnRequest(ctx context.Context, req *tts.SynthesisRequest, modelsDir string) error {
	if req == nil {
		return nil
	}
	return bindShippedClone(ctx, &req.ReferenceAudio, &req.ReferenceTranscript, req.Voice, modelsDir)
}

func bindShippedClone(ctx context.Context, refAudio, refText *string, voiceName, modelsDir string) error {
	sv, ok := managedqwen.LookupShipped(voiceName)
	if !ok {
		return nil
	}
	path, err := managedqwen.MaterializeShipped(ctx, sv, modelsDir)
	if err != nil {
		return err
	}
	if refAudio != nil && strings.TrimSpace(*refAudio) == "" {
		*refAudio = path
	}
	if refText != nil && strings.TrimSpace(*refText) == "" {
		*refText = sv.RefText
	}
	return nil
}

func appendShippedVoiceNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		seen[strings.ToLower(n)] = struct{}{}
	}
	for _, s := range managedqwen.ShippedVoices() {
		if _, ok := seen[strings.ToLower(s.Name)]; ok {
			continue
		}
		names = append(names, s.Name)
	}
	return names
}
