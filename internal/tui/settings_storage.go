package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var storageSeq atomic.Int64

// storageState is the Storage section's off-thread work: measuring the models
// folder and moving it. seq fences results from work that was cancelled.
type storageState struct {
	usage     string
	measuring bool
	moving    bool
	seq       int64
	cancel    context.CancelFunc
	ctx       context.Context
	events    *eventBridge
	measure   func(context.Context, string) (int64, int, error)
	move      func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error)
}

type storageUsageMsg struct {
	seq   int64
	bytes int64
	files int
	err   error
}

type storageMoveProgressMsg struct {
	seq      int64
	progress config.MoveProgress
}

type storageMoveDoneMsg struct {
	seq    int64
	result config.MoveResult
	err    error
}

func (s *storageState) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.seq = storageSeq.Add(1)
	s.cancel, s.events, s.measuring, s.moving = nil, nil, false, false
}

func (m settingsModel) storageRows() []settingRow {
	s := m.storage
	blocked := m.storageMoveBlocked()
	size := s.usage
	switch {
	case s.measuring:
		size = "measuring…"
	case size == "":
		size = "not measured"
	}
	rows := []settingRow{
		{key: "models_dir", name: "Models folder", kind: rowText, value: config.ModelsDirFrom(m.cfg), applies: appliesNextConversation, disabled: s.moving, reason: "A move is in progress",
			help: "Every speech, voice and decision model is stored here. Typing a folder here starts using the models already in it and moves nothing; use Move models to take the files along.",
			run: func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editDecision, "Models folder", config.ModelsDirFrom(m.cfg))
				m.edit.decisionKey = "models_dir"
				return cmd
			}},
		{key: "storage_usage", name: "Size on disk", kind: rowAction, value: size, verb: "measure", disabled: s.measuring || s.moving,
			help: "Counts the files in the models folder.",
			run:  func(m *settingsModel) tea.Cmd { return m.startStorageUsage() }},
		{key: "storage_move", name: "Move models to another folder…", kind: rowAction, verb: "choose a folder", disabled: blocked != "" || s.moving,
			reason: map[bool]string{true: blocked, false: "A move is in progress"}[blocked != ""],
			help:   "Moves every model to an empty or new folder, such as one on a larger drive, and uses it from now on. The current folder is removed only after the new one is complete; cancelling keeps the current folder in use.",
			run: func(m *settingsModel) tea.Cmd {
				return m.beginEdit(editStorageMove, "Move models to", "")
			}},
	}
	if s.moving {
		rows = append(rows, settingRow{key: "storage_cancel", name: "Cancel move", kind: rowAction, help: "Stops the copy and removes what was copied. The current folder stays in use.", run: func(m *settingsModel) tea.Cmd {
			m.storage.stop()
			m.message = "Move cancelled; the models are still in " + config.ModelsDirFrom(m.cfg)
			return nil
		}})
	}
	return rows
}

// storageMoveBlocked says why the models folder cannot be moved right now, or
// "" when it can: nothing may be reading from it or downloading into it.
func (m settingsModel) storageMoveBlocked() string {
	switch {
	case m.modelsInUse:
		return "End the conversation or meeting first; it is reading these files"
	case m.speechSetup.running, m.decisionSetup.running, m.qwenInstalling:
		return "Wait for the model download to finish, or cancel it"
	}
	return ""
}

func (m *settingsModel) startStorageUsage() tea.Cmd {
	if m.storage.measuring || m.storage.moving {
		return nil
	}
	m.storage.stop()
	ctx, cancel := context.WithCancel(context.Background())
	m.storage.cancel, m.storage.ctx, m.storage.measuring = cancel, ctx, true
	seq, dir, measure := m.storage.seq, config.ModelsDirFrom(m.cfg), m.storage.measure
	if measure == nil {
		measure = config.ModelsUsage
	}
	return func() tea.Msg {
		defer cancel()
		bytes, files, err := measure(ctx, dir)
		return storageUsageMsg{seq: seq, bytes: bytes, files: files, err: err}
	}
}

func (m *settingsModel) startStorageMove(dest string) tea.Cmd {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		m.message = "Cancelled"
		return nil
	}
	if m.storage.moving {
		return nil
	}
	if reason := m.storageMoveBlocked(); reason != "" {
		m.message = reason
		return nil
	}
	m.storage.stop()
	ctx, cancel := context.WithCancel(context.Background())
	m.storage.cancel, m.storage.ctx, m.storage.moving = cancel, ctx, true
	m.storage.events = newEventBridge(16)
	events, seq, cfg, move := m.storage.events, m.storage.seq, m.cfg, m.storage.move
	if move == nil {
		move = config.MoveModels
	}
	m.message = "Moving models…"
	run := func() tea.Msg {
		defer cancel()
		lastPhase, lastPct := "", int64(-1)
		result, err := move(ctx, cfg.Clone(), dest, config.MoveOptions{Progress: func(p config.MoveProgress) {
			pct := int64(0)
			if p.Total > 0 {
				pct = p.Done * 100 / p.Total
			}
			if p.Phase == lastPhase && pct == lastPct {
				return
			}
			lastPhase, lastPct = p.Phase, pct
			events.send(storageMoveProgressMsg{seq: seq, progress: p})
		}})
		return storageMoveDoneMsg{seq: seq, result: result, err: err}
	}
	return tea.Batch(events.waitContext(ctx), run)
}

func (m *settingsModel) applyStorage(msg tea.Msg) tea.Cmd {
	s := &m.storage
	switch msg := msg.(type) {
	case storageUsageMsg:
		if msg.seq != s.seq || !s.measuring {
			return nil
		}
		s.stop()
		if msg.err != nil {
			s.usage = ""
			m.message = "Could not measure the models folder: " + msg.err.Error()
			return nil
		}
		s.usage = storageUsage(msg.bytes, msg.files)
	case storageMoveProgressMsg:
		if msg.seq != s.seq || !s.moving {
			return nil
		}
		p := msg.progress
		m.message = "Moving models: " + p.Phase + "…"
		if p.Phase == config.MovePhaseCopy && p.Total > 0 {
			m.message = fmt.Sprintf("Moving models: %s of %s (%d%%)", config.FormatBytes(p.Done), config.FormatBytes(p.Total), p.Done*100/p.Total)
		}
		return s.events.waitContext(s.ctx)
	case storageMoveDoneMsg:
		current := msg.seq == s.seq && s.moving
		// A move that finished did move the files, even if it was cancelled
		// or Settings was left in its last moments; it always applies.
		if !current && msg.err != nil {
			return nil
		}
		if current {
			s.stop()
		}
		if msg.err != nil {
			m.message = "Models were not moved: " + msg.err.Error()
			if errors.Is(msg.err, context.Canceled) {
				m.message = "Move cancelled; the models are still in " + config.ModelsDirFrom(m.cfg)
			}
			return nil
		}
		msg.result.ApplyTo(m.cfg)
		s.usage = storageUsage(msg.result.Bytes, msg.result.Files)
		m.message = fmt.Sprintf("Moved %s to %s", config.FormatBytes(msg.result.Bytes), msg.result.To)
		if n := len(msg.result.RewrittenKeys); n > 0 {
			m.message += fmt.Sprintf(" · updated %s", strings.Join(msg.result.RewrittenKeys, ", "))
		}
		if len(msg.result.Warnings) > 0 {
			m.message += " · " + strings.Join(msg.result.Warnings, "; ")
		}
	}
	return nil
}

func storageUsage(bytes int64, files int) string {
	return fmt.Sprintf("%s in %d files", config.FormatBytes(bytes), files)
}
