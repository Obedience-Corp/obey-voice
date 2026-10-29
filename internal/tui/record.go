package tui

import (
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func (m *conversationModel) recordMode() string {
	if m.capture == nil {
		return capture.ModeOff
	}
	return m.capture.Mode()
}

func (m *conversationModel) configureRecord(args []string) {
	if len(args) > 1 {
		m.commandError("usage: /record [off|talk|lab]")
		return
	}
	cur := m.recordMode()
	action := "status"
	if len(args) == 1 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "status":
		if cur == capture.ModeOff {
			m.commandNotice("recording off — /record talk or /record lab")
			return
		}
		h := m.capture.Health()
		m.commandNotice(fmt.Sprintf("recording %s [%s] → %s (%d bytes; written seq %d, synced %d; dropped %d, omitted %d, scope excluded %d, errors %d)", cur, m.capture.ID(), m.capture.EventsPath(), h.Bytes, h.WrittenSequence, h.SyncedSequence, h.Dropped, h.Skipped, h.ScopeExcluded, h.WriteErrors))
	case "off", "stop":
		m.captureNextMode = ""
		if m.capture == nil {
			m.commandNotice("recording off")
			return
		}
		m.stopCapture()
	case "talk":
		m.startCapture(capture.ModeTalk)
	case "lab":
		m.startCapture(capture.ModeLab)
	case "toggle":
		if cur == capture.ModeOff {
			m.startCapture(capture.ModeTalk)
			return
		}
		m.stopCapture()
	default:
		m.commandError("usage: /record [off|talk|lab]")
	}
}

func (m *conversationModel) toggleRecordTalk() {
	if m.vim.enabled {
		return
	}
	if m.recordMode() == capture.ModeOff {
		m.startCapture(capture.ModeTalk)
		return
	}
	m.stopCapture()
}

func (m *conversationModel) startCapture(mode string) {
	m.pollCapture()
	if m.capturePending != nil {
		m.commandError("previous capture is still sealing; recording remains off")
		return
	}
	if m.capture != nil {
		if m.capture.Mode() == mode {
			m.commandNotice(fmt.Sprintf("already recording %s → %s", mode, m.capture.Dir()))
			return
		}
		m.stopCapture()
		m.captureNextMode = mode
		m.commandNotice("previous capture sealing; the requested recording starts after seal")
		return
	}
	sessionID := m.sessionID
	if sessionID == "" {
		sessionID = m.deps.sessionID
	}
	trace := events.TraceContext{SessionID: sessionID}
	if source, ok := m.deps.runner.(interface{ CaptureTrace() events.TraceContext }); ok {
		trace = source.CaptureTrace()
	}
	environment := map[string]string{}
	if cfg := m.cfg; cfg != nil {
		environment["brain_provider"] = cfg.BrainProvider
		environment["brain_model"] = config.BrainModelFor(cfg, cfg.BrainProvider)
		environment["stt_provider"], environment["stt_mode"], environment["stt_model"] = captureSTTEnvironment(cfg)
		environment["tts_provider"] = cfg.TTSProvider
		environment["tts_tier"] = cfg.QwenTTSModelTier

	}
	if build, ok := debug.ReadBuildInfo(); ok {
		environment["build_version"] = build.Main.Version
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				environment["build_revision"] = setting.Value
			}
			if setting.Key == "vcs.modified" {
				environment["build_modified"] = setting.Value
			}
		}
	}
	s, err := capture.Start(capture.Options{
		Environment:           environment,
		Trace:                 trace,
		Root:                  config.CapturesDir(),
		SessionID:             sessionID,
		Mode:                  mode,
		MidTurn:               m.turnState != turnIdle && m.turnState != 0,
		Bus:                   m.deps.bus,
		ExternalDebugAudio:    audio.DebugAudioDir(),
		ExternalTranscriptLog: "",
	})
	if err != nil {
		m.commandError("record: " + err.Error())
		return
	}
	m.capture = s
	m.commandNotice(fmt.Sprintf("recording %s [%s] → %s", mode, s.ID(), s.EventsPath()))
}

func (m *conversationModel) stopCapture() {
	m.captureNextMode = ""
	if m.capture == nil {
		return
	}
	s := m.capture
	m.capture = nil
	m.capturePending = s
	m.captureSealStarted = time.Now()
	m.captureSealWarned = false
	s.SealAsync(m.captureTurns())
	m.commandNotice(fmt.Sprintf("sealing %s → %s", s.Mode(), s.Dir()))
}

func (m *conversationModel) captureTurns() []capture.Turn {
	out := make([]capture.Turn, 0, len(m.plainTurns))
	for _, t := range m.plainTurns {
		out = append(out, capture.Turn{Role: t.role, Content: t.text, Speaker: t.speaker})
	}
	return out
}

type capturePollMsg struct{}

func (m *conversationModel) capturePollCmd() tea.Cmd {
	if m.capturePollScheduled || (m.capture == nil && m.capturePending == nil) {
		return nil
	}
	m.capturePollScheduled = true
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return capturePollMsg{} })
}
func (m *conversationModel) pollCapture() {
	if s := m.capturePending; s != nil {
		select {
		case <-s.Done():
			if err := s.SealError(); err != nil {
				m.commandError("capture incomplete/unsealed: " + err.Error() + " → " + s.Dir())
			} else {
				m.commandNotice("sealed " + s.Mode() + " → " + s.Dir())
			}
			m.capturePending = nil
			nextMode := m.captureNextMode
			m.captureNextMode = ""
			if nextMode != "" && s.SealError() == nil {
				m.startCapture(nextMode)
			}
		default:
			if !m.captureSealWarned && time.Since(m.captureSealStarted) >= 2*time.Second {
				m.captureSealWarned = true
				m.commandError("capture incomplete/unsealed: drain exceeded 2 seconds → " + s.Dir())
			}
		}
	}
	if s := m.capture; s != nil && time.Since(m.captureHealthLast) >= time.Second {
		m.captureHealthLast = time.Now()
		if source, ok := m.deps.runner.(interface{ CaptureTrace() events.TraceContext }); ok && !s.Matches(source.CaptureTrace()) {
			m.commandNotice("runtime changed; recording off")
			m.stopCapture()
			return
		}
		h := s.Health()
		if h.Limited || h.WriteErrors > 0 {
			m.commandError("capture stopped/incomplete: storage limit or write error → " + s.Dir())
			m.stopCapture()
		}
	}
}

// Match the provider factory's alias/default/mode resolution without mutating
// user configuration. Invalid/unresolved model metadata remains unavailable.
func captureSTTEnvironment(cfg *config.Config) (provider, mode, model string) {
	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		return "unresolved", "", ""
	}
	provider, mode = norm.Provider, norm.Mode
	switch norm.Provider {
	case config.STTProviderParakeet:
		if asset, err := config.ParakeetModel(cfg.ParakeetModel); err == nil {
			model = asset.Name
		}
	case config.STTProviderSherpa:
		if mode == config.STTModeStreaming {
			if asset, err := config.SherpaStreamingModel(cfg.SherpaStreamingModel); err == nil {
				model = asset.Name
			}
		} else {
			model, _ = config.SherpaOfflineWhisperModel(cfg.WhisperModel)
		}
	case config.STTProviderWhisperCPP:
		if asset, err := config.WhisperCPPModelAsset(cfg.WhisperCPPModel); err == nil {
			model = asset.Name
		}
	case config.STTProviderTranscribeCPP:
		if asset, err := config.TranscribeCPPModel(cfg.TranscribeCPPModel); err == nil {
			model = asset.Name
		}
	}
	return provider, mode, model
}
