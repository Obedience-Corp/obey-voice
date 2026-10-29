package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
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
	p.previewing = ""
	p.previewID++ // Retire completions even when no replacement preview starts.
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
		msg := voicePreviewDoneMsg{id: id, voice: voice.Name}
		err := runVoicePreview(ctx, voice, player, cfg, ensure, newTTS)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return msg
		}
		if err != nil {
			msg.message = err.Error()
		} else {
			msg.message = fmt.Sprintf("Previewed %s", voice.Name)
		}
		return msg
	}
}

// runVoicePreview is shared by terminal previews and the native app bridge.
// The caller owns the player and must drain this call before closing it.
func runVoicePreview(ctx context.Context, voice tts.Voice, player audio.Engine,
	cfg *config.Config,
	ensure func(context.Context, *config.Config, func(string, float64)) error,
	newTTS func(*config.Config) (tts.Provider, func(), error),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ensure == nil {
		ensure = func(ctx context.Context, cfg *config.Config, _ func(string, float64)) error {
			return config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil)
		}
	}
	if err := ensure(ctx, cfg, nil); err != nil {
		return fmt.Errorf("asset error: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if newTTS == nil {
		newTTS = ttsproviders.NewProvider
	}
	provider, cleanup, err := newTTS(cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return fmt.Errorf("TTS error: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // Stop synthesis before provider cleanup on a failed sample.
	stream, err := provider.Synthesize(ctx, tts.SpokenPreviewLine(voice.Name))
	if err != nil {
		return fmt.Errorf("synthesize error: %w", err)
	}
	stream, err = completeVoicePreview(ctx, stream)
	if err != nil {
		return fmt.Errorf("synthesize error: %w", err)
	}
	playing, err := player.PlayStream(ctx, stream)
	if err != nil {
		return fmt.Errorf("playback error: %w", err)
	}
	var result audio.PlaybackResult
	select {
	case <-ctx.Done():
		<-playing.Done()
		return ctx.Err()
	case result = <-playing.Done():
	}
	if result.Interrupted {
		return context.Canceled
	}
	if result.Err != nil {
		return fmt.Errorf("playback error: %w", result.Err)
	}
	return nil
}
