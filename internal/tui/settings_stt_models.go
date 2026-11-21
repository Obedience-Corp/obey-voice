package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	tea "github.com/charmbracelet/bubbletea"
)

type sttSetup struct {
	seq     int64
	running bool
	cancel  context.CancelFunc
	ctx     context.Context
	events  *eventBridge
	ensure  func(context.Context, *config.Config, config.AssetRequest, func(config.AssetProgress)) error
}
type sttSetupProgressMsg struct {
	seq      int64
	progress config.AssetProgress
}
type sttSetupDoneMsg struct {
	seq int64
	err error
}

func (s *sttSetup) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.seq = speechSetupSeq.Add(1)
	s.cancel, s.events, s.running = nil, nil, false
}

// The loader's source resolver includes non-empty environment bindings and aliases.
func sttModelOverrideReason(key string) string {
	if key == "whispercpp_model" && config.Source("whispercpp_model_path") == config.SourceEnv {
		return "WHISPERCPP_MODEL_PATH selects an external model file; unset it before choosing or installing a managed model"
	}
	if config.Source(key) == config.SourceEnv {
		spec, _ := config.SpecFor(key)
		return spec.Env + " overrides this model setting"
	}
	return ""
}

// Installation shares the scoped, verified installer used by models ensure --stt --vad.
func (m *settingsModel) startSTTSetup() tea.Cmd {
	if m.sttSetup.running {
		return nil
	}
	if spec, _, err := config.STTModelSetting(m.cfg); err == nil && spec.Key == "whispercpp_model" && config.Source("whispercpp_model_path") == config.SourceEnv {
		m.message = sttModelOverrideReason(spec.Key)
		return nil
	}
	m.sttSetup.stop()
	ctx, cancel := context.WithCancel(context.Background())
	s := &m.sttSetup
	s.cancel, s.ctx, s.running, s.events = cancel, ctx, true, newEventBridge(16)
	events, seq := s.events, s.seq
	ensure := s.ensure
	if ensure == nil {
		ensure = config.EnsureRuntimeAssetsProgress
	}
	cfg := m.cfg.Clone()
	req := config.ScopedAssetRequest(cfg, config.AssetScope{STT: true, VAD: true})
	m.message = "Installing selected speech model… · " + sttApplyNote
	if gap := config.STTEnvironmentGap(activeSTTProvider(cfg), cfg, config.ModelsDirFrom(cfg), m.binaryLookPath()); gap.Missing() {
		m.message += " · " + gap.Detail + " — " + gap.Remediation
	}
	return tea.Batch(events.waitContext(ctx), func() tea.Msg {
		defer cancel()
		err := ensure(ctx, cfg, req, func(p config.AssetProgress) {
			events.send(sttSetupProgressMsg{seq: seq, progress: p})
		})
		return sttSetupDoneMsg{seq: seq, err: err}
	})
}

func (m *settingsModel) applySTTSetup(msg tea.Msg) tea.Cmd {
	s := &m.sttSetup
	switch msg := msg.(type) {
	case sttSetupProgressMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		m.message = fmt.Sprintf("Speech model: %s — %s (%d%%)", msg.progress.Asset, msg.progress.Phase, int(msg.progress.Pct))
		return s.events.waitContext(s.ctx)
	case sttSetupDoneMsg:
		if msg.seq != s.seq || !s.running {
			return nil
		}
		s.stop()
		m.buildSTTItems()
		if msg.err != nil {
			m.message = fmt.Sprintf("Speech model installation failed: %v · Install / repair selected model to retry", msg.err)
		} else {
			m.message = "Selected speech model installed and verified · " + sttApplyNote
			if gap := config.STTEnvironmentGap(activeSTTProvider(m.cfg), m.cfg, config.ModelsDirFrom(m.cfg), m.binaryLookPath()); gap.Missing() {
				m.message = "Model installed; recognizer still needs setup: " + gap.Detail + " — " + gap.Remediation
			}
		}
	}
	return nil
}

func (m *settingsModel) selectSTTModel(key, value string) tea.Cmd {
	if m.sttSetup.running {
		return nil
	}
	spec, _, err := config.STTModelSetting(m.cfg)
	if err != nil || spec.Key != key {
		m.message = "Recognizer changed; choose its model again"
		return nil
	}
	if reason := sttModelOverrideReason(key); reason != "" {
		m.message = reason
		return nil
	}
	value = strings.TrimSpace(value)
	candidate := *m.cfg
	if err := config.SetSTTModel(&candidate, key, value); err != nil {
		m.message = err.Error()
		return nil
	}
	// Validate the actual asset before saving: a bad name must not start a download.
	if _, err := config.ManifestFor(&candidate, config.AssetRequest{NeedSTT: true}); err != nil {
		m.message = fmt.Sprintf("Not saved: %v", err)
		return nil
	}
	if err := m.save(key, value); err != nil {
		m.message = fmt.Sprintf("Could not save speech model: %v", err)
		return nil
	}
	_ = config.SetSTTModel(m.cfg, key, value)
	if key == "whispercpp_model" {
		if err := m.save("whispercpp_model_path", ""); err != nil {
			m.message = fmt.Sprintf("Model saved, but its previous file override could not be cleared: %v", err)
			return nil
		}
		m.cfg.WhisperCPPModelPath = ""
	}
	m.buildSTTItems()
	return m.startSTTSetup()
}

func (m settingsModel) sttModelRows() []settingRow {
	spec, value, err := config.STTModelSetting(m.cfg)
	if err != nil {
		return nil
	}
	label := value
	if name := spec.EnumLabels[value]; name != "" {
		label = name
	}
	row := settingRow{key: spec.Key, name: spec.Title, value: label, kind: rowText,
		help:    spec.Help + " Choosing a model installs it; existing verified files are reused.",
		applies: appliesNextConversation, disabled: m.sttSetup.running, reason: "Model installation is running",
		run: func(m *settingsModel) tea.Cmd {
			cmd := m.beginEdit(editSTTModel, spec.Title, value)
			m.edit.decisionKey = spec.Key
			return cmd
		},
	}
	if len(spec.Enum) > 0 {
		row.kind, row.current = rowPick, indexOf(spec.Enum, value)
		for _, choice := range spec.Enum {
			name := spec.EnumLabels[choice]
			if name == "" {
				name = choice
			}
			row.options = append(row.options, name)
		}
		row.set = func(m *settingsModel, i int) tea.Cmd { return m.selectSTTModel(spec.Key, spec.Enum[i]) }
	}
	rows := []settingRow{row}
	if len(spec.Suggestions) > 0 {
		rows = append(rows, settingRow{key: spec.Key + "_choices", name: "Available models", kind: rowPick, options: spec.Suggestions, current: indexOf(spec.Suggestions, value), disabled: m.sttSetup.running, reason: "Model installation is running", help: "Choose to download and use this model. The model field also accepts a custom supported name.", set: func(m *settingsModel, i int) tea.Cmd { return m.selectSTTModel(spec.Key, spec.Suggestions[i]) }})
	}
	if reason := sttModelOverrideReason(spec.Key); reason != "" {
		for i := range rows {
			rows[i].disabled, rows[i].reason = true, reason
		}
	}
	pathOverride := spec.Key == "whispercpp_model" && config.Source("whispercpp_model_path") == config.SourceEnv
	if pathOverride {
		rows[0].value = "External file: " + m.cfg.WhisperCPPModelPath
		rows = append(rows, settingRow{key: "whispercpp_effective_file", name: "Effective model file", kind: rowInfo, value: m.cfg.WhisperCPPModelPath, help: sttModelOverrideReason(spec.Key)})
	}
	rows = append(rows, settingRow{key: "stt_install", name: "Install / repair selected model", kind: rowAction,
		disabled: m.sttSetup.running, reason: "Model installation is running",
		help: "Downloads and verifies the selected speech model and voice activity detector. Reuses verified files. Retry here after a failed or canceled download.",
		run:  func(m *settingsModel) tea.Cmd { return m.startSTTSetup() },
	})
	if pathOverride {
		rows[len(rows)-1].disabled = true
		rows[len(rows)-1].reason = sttModelOverrideReason(spec.Key)
	}
	if m.sttSetup.running {
		rows = append(rows, settingRow{key: "stt_cancel", name: "Cancel model installation", kind: rowAction,
			run: func(m *settingsModel) tea.Cmd {
				m.sttSetup.stop()
				m.message = "Download canceled; Install / repair selected model to retry"
				return nil
			},
		})
	}
	return rows
}
