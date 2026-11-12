package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var storageSeq atomic.Int64

// storageState is the Storage section's off-thread work: measuring the models
// folder and moving it. The App shares one storageState with every Settings
// screen it opens, so a move outlives the screen that started it.
type storageState struct {
	usage     string
	measuring bool
	seq       int64
	cancel    context.CancelFunc
	// job is a move whose worker has not reported back. It is cleared only
	// by that report, so Move stays off until the worker has really ended.
	job *storageMove
	// confirmTo is the destination of a move waiting for y.
	confirmTo string
	// lifetime and wg let quitting wait for a move's cleanup.
	lifetime context.Context
	wg       *sync.WaitGroup
	measure  func(context.Context, string) (int64, int, error)
	runMove  func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error)
}

type storageMove struct {
	seq    int64
	ctx    context.Context
	cancel context.CancelFunc
	// pump ends when the worker returns, not on Cancel, so the phases a
	// cancelled move still goes through keep reaching the screen.
	pump       context.Context
	events     *eventBridge
	dest       string
	phase      string
	cancelling bool
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

func (s *storageState) moving() bool { return s != nil && s.job != nil }

func (s *storageState) stopMeasure() {
	if s.cancel != nil {
		s.cancel()
	}
	s.seq = storageSeq.Add(1)
	s.cancel, s.measuring = nil, false
}

// cancelMove asks a running move to stop. The job stays until its worker
// reports: before the save that undoes the copy, after it the move finishes.
func (s *storageState) cancelMove() {
	if s.job == nil || s.job.cancelling {
		return
	}
	s.job.cancelling = true
	s.job.cancel()
}

// stop ends everything the section started: leaving Settings or quitting.
func (s *storageState) stop() {
	if s == nil {
		return
	}
	s.stopMeasure()
	s.cancelMove()
	s.confirmTo = ""
}

func (m settingsModel) storageConfirming() bool {
	return m.storage != nil && m.storage.confirmTo != ""
}

func (m settingsModel) storageRows() []settingRow {
	s := m.storage
	if s == nil {
		s = &storageState{}
	}
	moving := s.moving()
	blocked := m.storageMoveBlocked()
	size := s.usage
	switch {
	case s.measuring:
		size = "measuring…"
	case size == "":
		size = "not measured"
	}
	moveReason := blocked
	if moving {
		moveReason = "A move is in progress"
	}
	rows := []settingRow{
		{key: "models_dir", name: "Models folder", kind: rowText, value: config.ModelsDirFrom(m.cfg), applies: appliesNextConversation, disabled: moving, reason: "A move is in progress",
			help: "Every speech, voice and decision model is stored here. Typing a folder here starts using the models already in it and moves nothing; use Move models to take the files along.",
			run: func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editDecision, "Models folder", config.ModelsDirFrom(m.cfg))
				m.edit.decisionKey = "models_dir"
				return cmd
			}},
		{key: "storage_usage", name: "Size on disk", kind: rowAction, value: size, verb: "measure", disabled: s.measuring || moving,
			help: "Counts the files in the models folder.",
			run:  func(m *settingsModel) tea.Cmd { return m.startStorageUsage() }},
		{key: "storage_move", name: "Move models to another folder…", kind: rowAction, verb: "choose a folder", disabled: moveReason != "", reason: moveReason,
			help: "Moves every model to an empty or new folder, such as one on a larger drive, and uses it from now on. You confirm before it starts. Cancel works until the new folder is saved; the old folder is removed only after every file is on disk in the new one.",
			run: func(m *settingsModel) tea.Cmd {
				return m.beginEdit(editStorageMove, "Move models to (full path)", "")
			}},
	}
	if moving {
		row := settingRow{key: "storage_cancel", name: "Cancel move", kind: rowAction,
			help: "Stops the move and removes what it copied, if the new folder is not saved yet. The current folder stays in use.",
			run:  func(m *settingsModel) tea.Cmd { m.cancelStorageMove(); return nil }}
		switch {
		case s.job.cancelling:
			row.name, row.disabled, row.reason = "Cancelling move…", true, "Waiting for the move to stop"
		case config.MovePhaseCommitted(s.job.phase):
			row.disabled, row.reason = true, "The new folder is saved; the move is finishing"
		}
		rows = append(rows, row)
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
	if m.storage.measuring || m.storage.moving() {
		return nil
	}
	m.storage.stopMeasure()
	ctx, cancel := context.WithCancel(context.Background())
	m.storage.cancel, m.storage.measuring = cancel, true
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

// armStorageMove asks for confirmation before a move to dest.
func (m *settingsModel) armStorageMove(dest string) {
	dest = strings.TrimSpace(dest)
	switch {
	case dest == "":
		m.message = "Cancelled"
	case m.storage.moving():
		m.message = "A move is still finishing"
	case m.storageMoveBlocked() != "":
		m.message = m.storageMoveBlocked()
	default:
		m.storage.confirmTo = dest
		m.message = ""
	}
}

// updateStorageMoveConfirm answers the armed move. Only y starts it.
func (m settingsModel) updateStorageMoveConfirm(msg tea.KeyMsg) (settingsModel, tea.Cmd) {
	dest := m.storage.confirmTo
	m.storage.confirmTo = ""
	if msg.String() != "y" && msg.String() != "Y" {
		m.message = "Kept the models in " + config.ModelsDirFrom(m.cfg)
		return m, nil
	}
	return m, m.startStorageMove(dest)
}

func (m settingsModel) confirmStorageMoveLines() []string {
	body := []string{
		normalStyle.Render("Move every model?"),
		"",
		dimStyle.Render("From  " + config.ModelsDirFrom(m.cfg)),
		dimStyle.Render("To    " + m.storage.confirmTo),
		"",
		dimStyle.Render("The files are copied (or renamed on the same drive), then the new folder"),
		dimStyle.Render("is saved, and only then is the old folder removed. Cancel works until"),
		dimStyle.Render("the save. Leaving Settings or quitting cancels the move too."),
		"",
		normalStyle.Render("Press ") + errorStyle.Render("y") + normalStyle.Render(" to move · any other key keeps them where they are"),
	}
	box := formBoxStyled(errorStyle.Render("Confirm move"), body, m.settingsBoxWidth(), errorStyle)
	return append([]string{""}, box...)
}

func (m *settingsModel) startStorageMove(dest string) tea.Cmd {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		m.message = "Cancelled"
		return nil
	}
	if m.storage.moving() {
		m.message = "A move is still finishing"
		return nil
	}
	if reason := m.storageMoveBlocked(); reason != "" {
		m.message = reason
		return nil
	}
	m.storage.stopMeasure()
	ctx, cancel := context.WithCancel(context.Background())
	pump, pumpDone := context.WithCancel(context.Background())
	job := &storageMove{seq: storageSeq.Add(1), ctx: ctx, cancel: cancel, pump: pump, events: newEventBridge(16), dest: dest}
	m.storage.job = job
	cfg, move := m.cfg, m.storage.runMove
	if move == nil {
		move = config.MoveModels
	}
	m.message = "Moving models…"
	run := func() tea.Msg {
		defer pumpDone()
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
			job.events.send(storageMoveProgressMsg{seq: job.seq, progress: p})
		}})
		return storageMoveDoneMsg{seq: job.seq, result: result, err: err}
	}
	return tea.Batch(job.events.waitContext(pump), trackedCommand(m.storage.lifetime, m.storage.wg, run))
}

func (m *settingsModel) cancelStorageMove() {
	job := m.storage.job
	if job == nil || job.cancelling {
		return
	}
	m.storage.cancelMove()
	if config.MovePhaseCommitted(job.phase) {
		m.message = "The new folder is already saved, so the move will finish; removing the old copy…"
		return
	}
	m.message = "Cancelling the move…"
}

func (m *settingsModel) applyStorage(msg tea.Msg) tea.Cmd {
	s := m.storage
	if s == nil {
		return nil
	}
	switch msg := msg.(type) {
	case storageUsageMsg:
		if msg.seq != s.seq || !s.measuring {
			return nil
		}
		s.stopMeasure()
		if msg.err != nil {
			s.usage = ""
			m.message = "Could not measure the models folder: " + msg.err.Error()
			return nil
		}
		s.usage = storageUsage(msg.bytes, msg.files)
	case storageMoveProgressMsg:
		job := s.job
		if job == nil || msg.seq != job.seq {
			return nil
		}
		p := msg.progress
		job.phase = p.Phase
		switch {
		case job.cancelling && config.MovePhaseCommitted(p.Phase):
			m.message = "The new folder is already saved, so the move will finish; removing the old copy…"
		case job.cancelling:
		case p.Phase == config.MovePhaseCopy && p.Total > 0:
			m.message = fmt.Sprintf("Moving models: %s of %s (%d%%)", config.FormatBytes(p.Done), config.FormatBytes(p.Total), p.Done*100/p.Total)
		default:
			m.message = "Moving models: " + p.Phase + "…"
		}
		return job.events.waitContext(job.pump)
	case storageMoveDoneMsg:
		job := s.job
		current := job != nil && msg.seq == job.seq
		if current {
			s.job = nil
		}
		// A move that finished did move the files, even if it was cancelled
		// or Settings was left in its last moments; it always applies.
		if msg.err != nil {
			if !current {
				return nil
			}
			m.message = "Models were not moved: " + msg.err.Error()
			if errors.Is(msg.err, context.Canceled) {
				m.message = "Move cancelled; the models are still in " + config.ModelsDirFrom(m.cfg)
			}
			return nil
		}
		msg.result.ApplyTo(m.cfg)
		s.usage = storageUsage(msg.result.Bytes, msg.result.Files)
		m.message = fmt.Sprintf("Moved %s to %s", config.FormatBytes(msg.result.Bytes), msg.result.To)
		if current && job.cancelling {
			m.message = "The new folder was already saved, so the move finished: " + m.message
		}
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

// modelsMoveBusy is shown when something that reads models is started while a
// move has not finished.
const modelsMoveBusy = "The models are being moved; wait for the move to finish (Settings → Storage)"

// onStorage delivers the Storage section's results whatever screen is showing:
// a move that finished after Settings was left still changed models_dir.
func (a App) onStorage(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := a.settings.applyStorage(msg)
	done, ok := msg.(storageMoveDoneMsg)
	if !ok {
		return a, cmd
	}
	if done.err == nil {
		done.result.ApplyTo(a.cfg)
	}
	if a.screen != screenSettings && a.settings.message != "" {
		a.launcher = a.launcher.withBanner(a.settings.message, done.err != nil && !errors.Is(done.err, context.Canceled))
	}
	return a, cmd
}
