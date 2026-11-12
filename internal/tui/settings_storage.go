package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

var storageSeq atomic.Int64

// storageState is the Storage section's off-thread work: measuring the models
// folder, checking a move and running it. The App shares one storageState
// with every Settings screen it opens, so a move outlives the screen that
// started it.
type storageState struct {
	usage     string
	measuring bool
	seq       int64
	cancel    context.CancelFunc
	// job is a move whose worker has not reported back. It is cleared only
	// by that report, so Move stays off until the worker has really ended.
	job *storageMove
	// checking is the plan in flight; confirm is a checked move waiting for y.
	checking int64
	confirm  *config.MovePlan
	measure  func(context.Context, string) (int64, int, error)
	plan     func(context.Context, *config.Config, string, config.MoveOptions) (config.MovePlan, error)
	runMove  func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error)
	personas func(*config.Config) ([]config.PersonaAssets, error)
	// fileConfig reads config.yaml as stored, without the active persona
	// laid over it.
	fileConfig func() (*config.Config, error)
}

// Worker states: a move is claimed by its worker or abandoned by a program
// that ended before the worker ran, never both.
const (
	movePending int32 = iota
	moveRunning
	moveAbandoned
)

type storageMove struct {
	seq    int64
	ctx    context.Context
	cancel context.CancelFunc
	// pump ends when the worker returns, not on Cancel, so the phases a
	// cancelled move still goes through keep reaching the screen.
	pump     context.Context
	finished chan struct{}
	state    atomic.Int32
	hangup   chan os.Signal
	events   *eventBridge
	dest     string
	// phase is set by the worker itself, so a dropped progress message can
	// never leave the screen behind what the move has done.
	phase      atomic.Value
	cancelling bool
}

func (j *storageMove) currentPhase() string {
	phase, _ := j.phase.Load().(string)
	return phase
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

// storageHangupMsg is the terminal closing while a move runs.
type storageHangupMsg struct{ seq int64 }

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
	s.checking, s.confirm = 0, nil
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
			help: "Moves every model to an empty or new folder, such as one on a larger drive, and uses it from now on. It is checked first and you confirm before it starts. Cancel works until the new folder is saved; the old folder is removed only after every file in it has been read and matched.",
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
		case config.MovePhaseCommitted(s.job.currentPhase()):
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

// moveConfig is the config a move is checked and run with: the file as
// stored, so the models its own settings use count as used even when the
// active persona speaks with others. The screen's config is the fallback.
func (s *storageState) moveConfig(screen *config.Config) *config.Config {
	read := s.fileConfig
	if read == nil {
		read = config.LoadRaw
	}
	if cfg, err := read(); err == nil && cfg != nil {
		return cfg
	}
	return screen
}

// movePersonas lists every persona's models, so models only a persona uses
// are not taken for someone else's files. A failure only means the old folder
// may be kept.
func (s *storageState) movePersonas(cfg *config.Config) []config.PersonaAssets {
	list := s.personas
	if list == nil {
		list = persona.AssetSources
	}
	personas, _ := list(cfg)
	return personas
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
	job := &storageMove{seq: storageSeq.Add(1), ctx: ctx, cancel: cancel, pump: pump, finished: make(chan struct{}),
		hangup: make(chan os.Signal, 1), events: newEventBridge(16), dest: dest}
	// A closed terminal sends SIGHUP; caught, it quits the program the normal
	// way, and the move's cleanup runs instead of the process dying mid-copy.
	signal.Notify(job.hangup, syscall.SIGHUP)
	m.storage.job = job
	screenCfg, move := m.cfg.Clone(), m.storage.runMove
	if move == nil {
		move = config.MoveModels
	}
	storage := m.storage
	m.message = "Moving models…"
	run := func() tea.Msg {
		defer close(job.finished)
		defer pumpDone()
		defer signal.Stop(job.hangup)
		defer cancel()
		if !job.state.CompareAndSwap(movePending, moveRunning) {
			return storageMoveDoneMsg{seq: job.seq, err: context.Canceled}
		}
		cfg := storage.moveConfig(screenCfg)
		personas := storage.movePersonas(cfg)
		lastPhase, lastPct := "", int64(-1)
		result, err := move(ctx, cfg, dest, config.MoveOptions{Personas: personas, Progress: func(p config.MoveProgress) {
			job.phase.Store(p.Phase)
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
	hangup := func() tea.Msg {
		select {
		case <-job.hangup:
			return storageHangupMsg{seq: job.seq}
		case <-pump.Done():
			return nil
		}
	}
	return tea.Batch(job.events.waitContext(pump), hangup, run)
}

// finishStorageMove runs when the program has ended, by any route: a quit
// key, a signal Bubble Tea turned into a quit, or a closed terminal. It
// cancels a move still running and waits for its cleanup, with no time limit,
// before the rest of teardown arms its force-quit. Ctrl-C ends the wait.
func finishStorageMove(s *storageState, say func(string)) {
	if s == nil {
		return
	}
	job := s.job
	s.stop()
	if job == nil {
		return
	}
	if job.state.CompareAndSwap(movePending, moveAbandoned) {
		signal.Stop(job.hangup)
		return
	}
	select {
	case <-job.finished:
		return
	default:
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(interrupt)
	if config.MovePhaseCommitted(job.currentPhase()) {
		say("Finishing the models move: the new folder is saved and the old copy is being checked and removed. Ctrl-C quits now and leaves it behind.")
	} else {
		say("Stopping the models move and removing what it copied. Ctrl-C quits now and leaves that copy behind.")
	}
	for {
		select {
		case <-job.finished:
			return
		case sig := <-interrupt:
			if sig == os.Interrupt {
				return
			}
		}
	}
}

func (m *settingsModel) cancelStorageMove() {
	job := m.storage.job
	if job == nil || job.cancelling {
		return
	}
	m.storage.cancelMove()
	if config.MovePhaseCommitted(job.currentPhase()) {
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
	case storagePlanMsg:
		m.applyStoragePlan(msg)
	case storageMoveProgressMsg:
		job := s.job
		if job == nil || msg.seq != job.seq {
			return nil
		}
		p := msg.progress
		switch {
		case job.cancelling && config.MovePhaseCommitted(job.currentPhase()):
			m.message = "The new folder is already saved, so the move will finish; removing the old copy…"
		case job.cancelling:
		case p.Total > 0:
			m.message = fmt.Sprintf("Moving models: %s: %s of %s (%d%%)", p.Phase, config.FormatBytes(p.Done), config.FormatBytes(p.Total), p.Done*100/p.Total)
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
	storage := a.settings.storage
	if storage == nil {
		return a, nil
	}
	if hangup, ok := msg.(storageHangupMsg); ok {
		if storage.job != nil && storage.job.seq == hangup.seq {
			return a.onQuit(quitMsg{})
		}
		return a, nil
	}
	done, isDone := msg.(storageMoveDoneMsg)
	current := isDone && storage.job != nil && storage.job.seq == done.seq
	cmd := a.settings.applyStorage(msg)
	if !isDone {
		return a, cmd
	}
	if done.err == nil {
		done.result.ApplyTo(a.cfg)
	}
	if current && a.screen != screenSettings && a.settings.message != "" {
		a.launcher = a.launcher.withBanner(a.settings.message, done.err != nil && !errors.Is(done.err, context.Canceled))
	}
	return a, cmd
}
