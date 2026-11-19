package tui

import (
	"context"
	"fmt"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var speechSetupSeq atomic.Int64

type speechSetup struct {
	seq     int64
	running bool
	cancel  context.CancelFunc
	ctx     context.Context
	events  *eventBridge
	ensure  func(context.Context, *config.Config, config.AssetRequest, func(config.AssetProgress)) error
}

type speechSetupProgressMsg struct {
	seq      int64
	progress config.AssetProgress
}

type speechSetupDoneMsg struct {
	multilingual bool
	seq          int64
	err          error
}

func (s *speechSetup) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.seq = speechSetupSeq.Add(1)
	s.cancel, s.events, s.running = nil, nil, false
}

// startSpeechSetup uses the same install contract as models ensure --tts.
// Browsing Settings remains read-only; choosing the engine or the explicit
// refresh action starts the download and verifies the returned voice list.
func (m *settingsModel) startSpeechSetup() tea.Cmd {
	return m.installSpeechCatalog(m.cfg.OpenAISpeechMultilingual)
}

func (m *settingsModel) installSpeechCatalog(multilingual bool) tea.Cmd {
	if m.speechSetup.running {
		return nil
	}
	m.speechSetup.stop()
	ctx, cancel := context.WithCancel(context.Background())
	m.speechSetup.cancel, m.speechSetup.running, m.speechSetup.ctx = cancel, true, ctx
	m.speechSetup.events = newEventBridge(16)
	events, seq := m.speechSetup.events, m.speechSetup.seq
	ensure := m.speechSetup.ensure
	if ensure == nil {
		ensure = config.EnsureRuntimeAssetsProgress
	}
	cfg := *m.cfg
	cfg.OpenAISpeechMultilingual = multilingual
	m.message = "Checking speech server and installing voices…"
	install := func() tea.Msg {
		defer cancel()
		err := ensure(ctx, &cfg, config.AssetRequest{NeedTTS: true, NeedVoiceCatalog: true}, func(p config.AssetProgress) {
			events.send(speechSetupProgressMsg{seq: seq, progress: p})
		})
		return speechSetupDoneMsg{seq: seq, multilingual: multilingual, err: err}
	}
	return tea.Batch(events.waitContext(ctx), install)
}

func (m *settingsModel) applySpeechSetup(msg tea.Msg) tea.Cmd {
	s := &m.speechSetup
	switch msg := msg.(type) {
	case speechSetupProgressMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		m.message = fmt.Sprintf("Voice setup: %s — %s (%d%%)", msg.progress.Asset, msg.progress.Phase, int(msg.progress.Pct))
		return s.events.waitContext(s.ctx)
	case speechSetupDoneMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		s.stop()
		if msg.err != nil {
			m.message = fmt.Sprintf("Voice setup failed: %v", msg.err)
			return nil
		}
		if msg.multilingual != m.cfg.OpenAISpeechMultilingual {
			if err := m.save("openai_speech_multilingual", msg.multilingual); err != nil {
				m.message = fmt.Sprintf("Could not save voice catalog: %v", err)
				return nil
			}
			m.cfg.OpenAISpeechMultilingual = msg.multilingual
			m.voiceLocale = ""
		}
		m.message = "Voice setup complete"
		m.voices.reset()
		return m.loadVoiceCatalog()
	}
	return nil
}

func (m settingsModel) speechSetupRows() []settingRow {
	if activeTTSProvider(m.cfg) != config.OpenAISpeechProviderName {
		return nil
	}
	return []settingRow{{
		name: "Install / refresh voices", kind: rowAction, verb: "install",
		help:     "Checks the speech server and downloads or refreshes Phonon's named voices, with progress.",
		disabled: m.speechSetup.running, reason: "Voice setup is running…",
		run: func(m *settingsModel) tea.Cmd { return m.startSpeechSetup() },
	}}
}

func (m settingsModel) speechCatalogRows() []settingRow {
	if activeTTSProvider(m.cfg) != config.OpenAISpeechProviderName {
		return nil
	}
	return []settingRow{{
		key: "openai_speech_multilingual", name: "Full Phonon catalog (102 voices)", kind: rowToggle,
		value: onOff(m.cfg.OpenAISpeechMultilingual), applies: appliesNextConversation,
		help:     "Downloads Phonon 40M multilingual for noncommercial use (CC BY-NC 4.0). Turn off to use your configured speech model.",
		disabled: m.speechSetup.running, reason: "Voice setup is running…",
		run: func(m *settingsModel) tea.Cmd { return m.installSpeechCatalog(!m.cfg.OpenAISpeechMultilingual) },
	}}
}
