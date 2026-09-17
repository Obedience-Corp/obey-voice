package tui

import (
	"fmt"
	"strings"

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
		m.commandNotice(fmt.Sprintf("recording %s → %s", cur, m.capture.Dir()))
	case "off", "stop":
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
	if m.capture != nil {
		if m.capture.Mode() == mode {
			m.commandNotice(fmt.Sprintf("already recording %s → %s", mode, m.capture.Dir()))
			return
		}
		m.stopCapture()
	}
	sessionID := m.sessionID
	if sessionID == "" {
		sessionID = m.deps.sessionID
	}
	s, err := capture.Start(capture.Options{
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
	m.commandNotice(fmt.Sprintf("recording %s → %s", mode, s.Dir()))
}

func (m *conversationModel) stopCapture() {
	if m.capture == nil {
		return
	}
	dir := m.capture.Dir()
	mode := m.capture.Mode()
	if err := m.capture.Stop(m.captureTurns()); err != nil {
		m.commandError("record stop: " + err.Error())
		m.capture = nil
		return
	}
	m.capture = nil
	m.commandNotice(fmt.Sprintf("sealed %s → %s", mode, dir))
}

func (m *conversationModel) captureTurns() []capture.Turn {
	out := make([]capture.Turn, 0, len(m.plainTurns))
	for _, t := range m.plainTurns {
		out = append(out, capture.Turn{Role: t.role, Content: t.text, Speaker: t.speaker})
	}
	return out
}
