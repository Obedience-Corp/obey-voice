package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// voicePreview is the one preview player Settings and the persona form share.
// Stale-completion gating, player reuse, and cancel live here so the two
// screens cannot drift.
type voicePreview struct {
	previewing       string
	previewID        int64
	previewCancel    context.CancelFunc
	previewPlayer    audio.Engine
	newPreviewPlayer func() audio.Engine
	ensure           func(context.Context, *config.Config, func(string, float64)) error
	newTTS           func(*config.Config) (tts.Provider, func(), error)
}

type voicePreviewDoneMsg struct {
	id      int64
	voice   string
	message string
}

func (p *voicePreview) cancel() {
	if p == nil {
		return
	}
	if p.previewCancel != nil {
		p.previewCancel()
		p.previewCancel = nil
	}
	if p.previewPlayer != nil {
		p.previewPlayer.Stop()
	}
}

func (p *voicePreview) close() {
	if p == nil {
		return
	}
	p.cancel()
	if p.previewPlayer != nil {
		_ = p.previewPlayer.Close()
		p.previewPlayer = nil
	}
}

func (p *voicePreview) player() audio.Engine {
	if p.previewPlayer != nil {
		return p.previewPlayer
	}
	if p.newPreviewPlayer != nil {
		p.previewPlayer = p.newPreviewPlayer()
		return p.previewPlayer
	}
	p.previewPlayer = playback.NewPlayer()
	return p.previewPlayer
}

func (p *voicePreview) applyDone(msg voicePreviewDoneMsg) bool {
	if p == nil || msg.id != p.previewID || msg.voice != p.previewing {
		return false
	}
	p.previewing = ""
	p.previewCancel = nil
	return true
}

func (p *voicePreview) start(cfg config.Config, voice tts.Voice) tea.Cmd {
	p.cancel()
	p.previewing = voice.Name
	p.previewID++
	ctx, cancel := context.WithCancel(context.Background())
	p.previewCancel = cancel
	player := p.player()
	applyPreviewVoice(&cfg, voice)
	return playVoicePreview(ctx, p.previewID, voice, player, &cfg, p.ensure, p.newTTS)
}

func applyPreviewVoice(cfg *config.Config, voice tts.Voice) {
	if cfg == nil {
		return
	}
	config.SetTTSVoiceFor(cfg, activeTTSProvider(cfg), voice.Name)
	if activeTTSProvider(cfg) == "qwen3-tts" {
		cfg.QwenTTSMode = string(tts.VoiceModeCustomVoice)
		if cfg.QwenTTSLanguage == "" {
			cfg.QwenTTSLanguage = "Auto"
		}
	}
}

func playVoicePreview(
	ctx context.Context,
	id int64,
	voice tts.Voice,
	player audio.Engine,
	cfg *config.Config,
	ensure func(context.Context, *config.Config, func(string, float64)) error,
	newTTS func(*config.Config) (tts.Provider, func(), error),
) tea.Cmd {
	return func() tea.Msg {
		quiet := voicePreviewDoneMsg{id: id, voice: voice.Name}
		if ensure == nil {
			ensure = func(ctx context.Context, cfg *config.Config, _ func(string, float64)) error {
				return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil)
			}
		}
		if err := ensure(ctx, cfg, nil); err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Asset error: %v", err)}
		}
		if newTTS == nil {
			newTTS = ttsproviders.NewProvider
		}
		ttsProvider, cleanup, err := newTTS(cfg)
		if err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("TTS error: %v", err)}
		}
		if cleanup != nil {
			defer cleanup()
		}
		stream, err := ttsProvider.Synthesize(ctx, tts.SpokenPreviewLine(voice.Name))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Synthesize error: %v", err)}
		}
		playback, err := player.PlayStream(ctx, stream)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return quiet
			}
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", err)}
		}
		var result audio.PlaybackResult
		select {
		case <-ctx.Done():
			<-playback.Done()
			return quiet
		case result = <-playback.Done():
		}
		if result.Interrupted || errors.Is(result.Err, context.Canceled) {
			return quiet
		}
		if result.Err != nil {
			return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Playback error: %v", result.Err)}
		}
		return voicePreviewDoneMsg{id: id, voice: voice.Name, message: fmt.Sprintf("Previewed %s", voice.Name)}
	}
}
