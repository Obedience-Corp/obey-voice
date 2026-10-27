package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestSpeechSetupUsesInstallRequestAndReloadsCatalog(t *testing.T) {
	m, fetch, _ := openaiSpeechSettings(t, "default")
	m, _ = m.Update(fetch()) // a cached catalog must be invalidated after setup
	m.speechSetup.ensure = func(ctx context.Context, cfg *config.Config, req config.AssetRequest, progress func(config.AssetProgress)) error {
		if cfg == m.cfg || cfg.TTSProvider != config.OpenAISpeechProviderName || !req.NeedTTS || !req.NeedVoiceCatalog {
			t.Fatalf("installer received shared/wrong config or request: %+v", req)
		}
		progress(config.AssetProgress{Asset: "phonon", Phase: "download", Pct: 50})
		return nil
	}
	batch := m.startSpeechSetup()().(tea.BatchMsg)
	if len(batch) != 2 || !m.speechSetup.running {
		t.Fatalf("setup commands = %d, running = %v", len(batch), m.speechSetup.running)
	}
	done := batch[1]()
	m, next := m.Update(done)
	if m.speechSetup.running || next == nil || !m.voices.loading {
		t.Fatalf("setup did not refresh catalog: running=%v loading=%v", m.speechSetup.running, m.voices.loading)
	}
	m, _ = m.Update(next())
	if m.voices.loading || len(m.shownVoiceItems()) != len(phononCatalog) {
		t.Fatal("catalog did not land")
	}
}

func TestSpeechSetupCancellationAndStaleCompletion(t *testing.T) {
	m, _, _ := openaiSpeechSettings(t, "default")
	recordSaves(&m)
	var installCtx context.Context
	m.speechSetup.ensure = func(ctx context.Context, _ *config.Config, _ config.AssetRequest, _ func(config.AssetProgress)) error {
		installCtx = ctx
		return ctx.Err()
	}
	batch := m.startSpeechSetup()().(tea.BatchMsg)
	seq := m.speechSetup.seq
	m.selectTTSProvider("kokoro")
	result := batch[1]()
	if !errors.Is(installCtx.Err(), context.Canceled) {
		t.Fatal("switching engine did not cancel setup")
	}
	m, cmd := m.Update(result)
	if cmd != nil || m.speechSetup.running || m.cfg.TTSProvider != "kokoro" {
		t.Fatal("stale completion affected the new engine")
	}
	m, cmd = m.Update(speechSetupProgressMsg{seq: seq})
	if cmd != nil {
		t.Fatal("stale progress restarted the event pump")
	}
	m.cfg.TTSProvider = config.OpenAISpeechProviderName
	m.startSpeechSetup()
	cancelled := m.speechSetup.ctx
	m.closePreview()
	if !errors.Is(cancelled.Err(), context.Canceled) || m.speechSetup.running {
		t.Fatal("closing Settings did not cancel setup")
	}
}
