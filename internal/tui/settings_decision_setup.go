package tui

import (
	"context"
	"fmt"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var decisionSetupSeq atomic.Int64

type decisionSetup struct {
	seq     int64
	running bool
	cancel  context.CancelFunc
	ctx     context.Context
	events  *eventBridge
	ensure  func(context.Context, *config.Config, config.AssetRequest, func(config.AssetProgress)) error
}

type decisionSetupProgressMsg struct {
	seq      int64
	progress config.AssetProgress
}

type decisionSetupDoneMsg struct {
	seq    int64
	err    error
	report string
}

func (s *decisionSetup) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.seq = decisionSetupSeq.Add(1)
	s.cancel, s.events, s.running = nil, nil, false
}

// Explicit setup runs off the UI thread and never enables routing.
func (m *settingsModel) startDecisionSetup(installModel bool) tea.Cmd {
	if m.decisionSetup.running {
		return nil
	}
	m.decisionSetup.stop()
	ctx, cancel := context.WithCancel(context.Background())
	m.decisionSetup.cancel, m.decisionSetup.running, m.decisionSetup.ctx = cancel, true, ctx
	m.decisionSetup.events = newEventBridge(16)
	events, seq := m.decisionSetup.events, m.decisionSetup.seq
	ensure := m.decisionSetup.ensure
	if ensure == nil {
		ensure = config.EnsureRuntimeAssetsProgress
	}
	cfg := config.DecisionSetupConfig(m.cfg)
	m.message = "Checking selected decision model…"
	install := func() tea.Msg {
		defer cancel()
		var err error
		if installModel {
			err = ensure(ctx, cfg, config.AssetRequest{NeedDecision: true}, func(p config.AssetProgress) {
				events.send(decisionSetupProgressMsg{seq: seq, progress: p})
			})
		}
		report := ""
		if err == nil {
			report, err = config.DecisionSetupStatus(cfg)
		}
		return decisionSetupDoneMsg{seq: seq, err: err, report: report}
	}
	return tea.Batch(events.waitContext(ctx), install)
}

func (m *settingsModel) applyDecisionSetup(msg tea.Msg) tea.Cmd {
	s := &m.decisionSetup
	switch msg := msg.(type) {
	case decisionSetupProgressMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		m.message = fmt.Sprintf("Decision setup: %s — %s (%d%%)", msg.progress.Asset, msg.progress.Phase, int(msg.progress.Pct))
		return s.events.waitContext(s.ctx)
	case decisionSetupDoneMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		s.stop()
		if msg.err != nil {
			m.message = fmt.Sprintf("Decision setup stopped (Install / repair to retry): %v", msg.err)
			return nil
		}
		m.message = msg.report
		return nil
	}
	return nil
}

func (m settingsModel) decisionSetupRows() []settingRow {
	busy := m.decisionSetup.running
	rows := []settingRow{{key: "decision_directory", name: "Model storage / existing models", kind: rowText, value: config.ModelsDirFrom(m.cfg), help: "Shared model root. Choose a fast drive or an existing Obey Voice model root. Existing files are verified and reused; changing this path does not move files.", disabled: busy, run: func(m *settingsModel) tea.Cmd {
		cmd := m.beginEdit(editDecision, "Model storage directory", config.ModelsDirFrom(m.cfg))
		m.edit.decisionKey = "models_dir"
		return cmd
	}}}
	for _, action := range []struct {
		key, name string
		install   bool
	}{{"decision_verify", "Check installed model / verify", false}, {"decision_install", "Install / repair selected model", true}} {
		rows = append(rows, settingRow{key: action.key, name: action.name, kind: rowAction, disabled: busy || (m.onDecisionSettings != nil && m.cfg.DecisionEnabled), reason: "Stop setup or turn off decision routing first", help: "Only the selected model is checked. Install reuses verified files and repairs missing or corrupt files. Does not enable routing.", run: func(m *settingsModel) tea.Cmd { return m.startDecisionSetup(action.install) }})
	}
	if busy {
		rows = append(rows, settingRow{key: "decision_cancel", name: "Cancel model setup", kind: rowAction, run: func(m *settingsModel) tea.Cmd {
			m.decisionSetup.stop()
			m.message = "Setup canceled; Install / repair resumes using verified files"
			return nil
		}})
	}
	return rows
}
