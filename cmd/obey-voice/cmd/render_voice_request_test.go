//go:build !integration

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/render"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type voiceRequestCapture struct{ request tts.SynthesisRequest }

var errVoiceCaptured = errors.New("request captured")

func (p *voiceRequestCapture) Synthesize(context.Context, string) (*audio.PCMStream, error) {
	return nil, errVoiceCaptured
}
func (p *voiceRequestCapture) Available() bool                       { return true }
func (p *voiceRequestCapture) ListVoices(string, string) []tts.Voice { return nil }
func (p *voiceRequestCapture) SynthesizeRequest(_ context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	p.request = req
	return tts.SynthesisResult{}, errVoiceCaptured
}

func TestRenderSendsSelectedProviderVoiceWithSavedQwenClone(t *testing.T) {
	for _, provider := range []string{config.OpenAISpeechProviderName, "kokoro", "qwen3-tts"} {
		t.Run(provider, func(t *testing.T) {
			cfg := &config.Config{TTSProvider: provider, TTSVoice: "af_heart", OpenAISpeechVoice: "Juniper", QwenTTSVoice: "VeronicaDesk"}
			opts := render.Options{Voice: "Harper"}
			if provider == "kokoro" {
				opts.Voice = "af_bella"
			}
			if provider == "qwen3-tts" {
				opts.Voice = "vivian"
			}
			wanted := opts.Voice
			applyVoiceOverrides(cfg, &opts)
			capture := &voiceRequestCapture{}
			synth := &ttsSynth{provider: capture, voice: config.TTSVoiceFor(cfg, provider)}
			_, _, err := synth.SynthesizeRequest(context.Background(), render.SynthesisRequest{Text: "Hello", Voice: opts.Voice})
			if !errors.Is(err, errVoiceCaptured) {
				t.Fatal(err)
			}
			if capture.request.Voice != wanted {
				t.Fatalf("sent %q, want %q", capture.request.Voice, wanted)
			}
		})
	}
}

func TestNativeVoicesRequireAssetsWithoutStartingRuntime(t *testing.T) {
	cfg := &config.Config{TTSProvider: config.OpenAISpeechProviderName, OpenAISpeechRuntime: "native", OpenAISpeechBackend: "cpu", ModelsDir: t.TempDir()}
	missing := missingTTSAssets(context.Background(), cfg)
	if len(missing) != 1 || !strings.Contains(missing[0], "Phonon native") {
		t.Fatalf("unexpected missing assets %v", missing)
	}
}
