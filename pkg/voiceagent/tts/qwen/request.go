package qwen

import (
	"context"
	"errors"
	"fmt"
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"os"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// SoftCancel asks the warm workers to abandon what they are synthesizing
// without killing the processes (stage A: between requests; stage B:
// mid-synth).
//
// The request id is ignored and the cancel reaches every live worker. With a
// pool that is the honest shape of it: barge-in ends the turn, and a segment
// still being synthesized for that turn is waste whichever worker holds it.
// Cancelling one index is the job's own context, not this call.
func (q *Qwen3TTS) SoftCancel(_ string) error {
	if q == nil || !q.alive.Load() {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "cancel", Kind: tts.ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if q.native {
		return q.cancelActiveAll()
	}
	// Managed Python worker has no soft-cancel control message yet.
	return nil
}

// Synthesize streams synthesized PCM frames for the given text.
func (q *Qwen3TTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := q.SynthesizeRequest(ctx, tts.SynthesisRequest{
		Text:                text,
		Voice:               q.voice,
		Mode:                q.mode,
		Language:            q.language,
		Instruction:         q.instruction,
		ReferenceAudio:      q.referenceAudio,
		ReferenceTranscript: q.referenceTranscript,
	})
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

// SynthesizeRequest validates provider-specific controls and converts the
// worker's 24 kHz WAV output into Obey Voice's existing float32 PCM stream.
func (q *Qwen3TTS) SynthesizeRequest(ctx context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !q.Available() {
		return tts.SynthesisResult{}, &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if strings.TrimSpace(req.Text) == "" {
		return tts.SynthesisResult{}, &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: tts.ProviderErrorInput, Err: errors.New("text is empty")}
	}
	if q.remote {
		// obey-speech/v1 carries no ref_wav: an explicit one is a caller error,
		// and a shipped clone (whose throat *is* a ref_wav) speaks in the
		// worker's own voice instead of failing the turn.
		if strings.TrimSpace(req.ReferenceAudio) != "" {
			return tts.SynthesisResult{}, qwenUnsupported("reference audio", "a speech worker on another computer cannot read this computer's audio files; set tts_place=local to clone")
		}
		if _, shipped := managedqwen.LookupShipped(req.Voice); shipped {
			req.Voice = ""
		}
	} else if err := bindShippedOnRequest(ctx, &req, q.modelsDir); err != nil {
		return tts.SynthesisResult{}, &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "bind shipped voice", Kind: tts.ProviderErrorInput, Err: err}
	}
	// Same spoken-text rules Kokoro gets: emoji stripped, years read as years.
	// Applied after the empty check above, so genuinely empty input is still a
	// caller error while text that merely *cleans* to nothing is not.
	req.Text = textclean.PrepareSpokenText(req.Text)
	if strings.TrimSpace(req.Text) == "" {
		// A reply of nothing but an emoji. Nothing to say is not a failure —
		// return a stream that closes with no frames, matching Kokoro.
		stream := audio.NewPCMStream(ctx)
		if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
			return tts.SynthesisResult{}, err
		}
		stream.Close()
		return tts.SynthesisResult{
			Stream:     stream,
			SampleRate: qwen3TTSSampleRate,
			Provider:   qwen3TTSProviderName,
			Model:      q.model,
			Voice:      q.voice,
			Mode:       tts.VoiceModeStatic,
		}, nil
	}
	if q.managed || q.native {
		if req.Mode == "" || req.Mode == tts.VoiceModeStatic {
			req.Mode = tts.VoiceModeCustomVoice
		}
		if strings.TrimSpace(req.Voice) == "" || strings.EqualFold(req.Voice, "default") {
			req.Voice = q.voice
		}
		if strings.TrimSpace(req.Language) == "" {
			req.Language = q.language
		}
		if voice, ok := managedqwen.CanonicalVoice(req.Voice); ok {
			req.Voice = voice
		}
		if language, ok := managedqwen.CanonicalLanguage(req.Language); ok {
			req.Language = language
		}
		if req.Speed != 0 {
			return tts.SynthesisResult{}, qwenUnsupported("speech speed", "the Qwen CustomVoice worker does not expose speed control")
		}
		if q.managed {
			if strings.TrimSpace(req.ReferenceAudio) != "" || strings.TrimSpace(req.ReferenceTranscript) != "" {
				return tts.SynthesisResult{}, qwenUnsupported("reference voice", "installable approved-clone support has not landed yet")
			}
		}
		// Native stage A supports preset + optional ref_wav; transcript not required by worker.
		if err := tts.ValidateRequest(q.Capabilities(), req); err != nil {
			return tts.SynthesisResult{}, err
		}
	} else {
		if req.SampleRate != 0 && req.SampleRate != qwen3TTSSampleRate {
			return tts.SynthesisResult{}, qwenUnsupported("sample rate", fmt.Sprintf("cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, qwen3TTSSampleRate))
		}
		if voice := strings.TrimSpace(req.Voice); voice != "" && !strings.EqualFold(voice, "default") {
			return tts.SynthesisResult{}, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the external native CLI; use the model's default voice", req.Voice))
		}
		if req.Speed != 0 {
			return tts.SynthesisResult{}, qwenUnsupported("speech speed", "unsupported by the external native CLI; omit speed")
		}
		if req.Mode != "" && req.Mode != tts.VoiceModeStatic {
			return tts.SynthesisResult{}, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the external native CLI", req.Mode))
		}
		if strings.TrimSpace(req.Language) != "" {
			return tts.SynthesisResult{}, qwenUnsupported("language", "language selection is not exposed by the external native CLI")
		}
		if strings.TrimSpace(req.Instruction) != "" {
			return tts.SynthesisResult{}, qwenUnsupported("voice instruction", "voice design is not exposed by the external native CLI")
		}
		if strings.TrimSpace(req.ReferenceAudio) != "" || strings.TrimSpace(req.ReferenceTranscript) != "" {
			return tts.SynthesisResult{}, qwenUnsupported("reference voice", "reference-audio cloning is not exposed by the external native CLI")
		}
	}
	if !q.native {
		if err := validateQwenReference(req); err != nil {
			return tts.SynthesisResult{}, err
		}
	} else if ref := strings.TrimSpace(req.ReferenceAudio); ref != "" && !q.remote {
		// Native worker accepts ref_wav without transcript; still bound file existence.
		if _, err := os.Stat(ref); err != nil {
			return tts.SynthesisResult{}, &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: fmt.Errorf("reference audio is unavailable")}
		}
	}
	if req.Mode == tts.VoiceModeApprovedClone && !q.consent {
		return tts.SynthesisResult{}, &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate request", Kind: tts.ProviderErrorInput, Err: errors.New("explicit consent is required for approved voice cloning")}
	}

	voice := req.Voice
	if voice == "" {
		voice = "default"
	}
	stream := audio.NewPCMStream(ctx)
	go q.synthesize(ctx, req, stream)

	resultMode := req.Mode
	if resultMode == "" {
		resultMode = tts.VoiceModeStatic
	}
	return tts.SynthesisResult{
		Stream:     stream,
		SampleRate: qwen3TTSSampleRate,
		Provider:   qwen3TTSProviderName,
		Model:      q.model,
		Voice:      voice,
		Mode:       resultMode,
	}, nil
}

func qwenUnsupported(feature, detail string) error {
	return &tts.ProviderError{
		Provider:  qwen3TTSProviderName,
		Operation: "validate request",
		Kind:      tts.ProviderErrorInput,
		Err:       &tts.UnsupportedFeatureError{Provider: qwen3TTSProviderName, Feature: feature, Detail: detail},
	}
}

func validateQwenReference(req tts.SynthesisRequest) error {
	path := strings.TrimSpace(req.ReferenceAudio)
	if path == "" {
		if strings.TrimSpace(req.ReferenceTranscript) != "" {
			return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference transcript requires reference audio")}
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: fmt.Errorf("reference audio is unavailable")}
	}
	if !info.Mode().IsRegular() {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference audio must be a regular file")}
	}
	if info.Size() > 50<<20 {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference audio exceeds the 50 MiB limit")}
	}
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil || len(samples) == 0 || rate <= 0 {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference audio must be a non-empty readable WAV")}
	}
	duration := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))
	if duration < time.Second || duration > 30*time.Second {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference audio duration must be between 1 and 30 seconds")}
	}
	if strings.TrimSpace(req.ReferenceTranscript) == "" {
		return &tts.ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: tts.ProviderErrorInput, Err: errors.New("reference transcript is required")}
	}
	return nil
}
