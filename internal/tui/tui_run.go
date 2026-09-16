package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// processForceExit ends a wedged TUI process after quit. The CLI signal path
// uses the same 130 status. Tests replace this so they do not kill the suite.
var processForceExit = os.Exit

// processForceQuitAfter caps remaining teardown after the session is saved.
// Same budget as drainTimeout / cmd.forceQuitTimeout.
var processForceQuitAfter = drainTimeout

// Run starts the launcher and live conversation. Returning to the menu drains
// and saves the session before opening a fresh launcher in the same process.
func Run(cfg *config.Config, build RuntimeBuilder) error {
	return RunWithMeeting(cfg, build, nil)
}

// RunWithMeeting is Run plus an optional MeetingBuilder for the launcher
// "Record meeting" entry.
func RunWithMeeting(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder) error {
	return run(cfg, build, meeting, false)
}

// RunConversation starts the TUI directly in the conversation screen —
// resume/continue land in the live conversation, not the launcher.
func RunConversation(cfg *config.Config, build RuntimeBuilder) error {
	return run(cfg, build, nil, true)
}

// DiagnosticsLogPath is the file this package redirects file descriptor 2 into
// while a Bubble Tea program owns the terminal. Callers that run their own
// program without that redirect (the standalone meeting recorder) send their
// diagnostics to the same file rather than to a raw stderr.
func DiagnosticsLogPath() string {
	dir := filepath.Join(config.ConfigDir(), "logs")
	if debugDir := audio.DebugAudioDir(); debugDir != "" {
		dir = debugDir
	}
	return filepath.Join(dir, "native-diagnostics.log")
}

func run(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder, startInConversation bool) error {
	return runPrograms(cfg, build, meeting, startInConversation, func(app App) (tea.Model, error) {
		return tea.NewProgram(app, tea.WithAltScreen(), tea.WithReportFocus()).Run()
	})
}

// Each program owns one conversation lifetime. Reusing teardown between chats
// keeps old turns, event pumps, and talk keys out of the next session.
func runPrograms(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder, startInConversation bool, program func(App) (tea.Model, error)) error {
	for {
		back, err := runProgram(cfg, build, meeting, startInConversation, program)
		if err != nil || !back {
			return err
		}
		startInConversation = false
	}
}

func runProgram(cfg *config.Config, build RuntimeBuilder, meeting MeetingBuilder, startInConversation bool, program func(App) (tea.Model, error)) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := NewApp(cfg)
	app.builder = build
	app.meetingBuilder = meeting
	app.meetingSweep = sweepMeetingRoutesCmd(cfg)
	app.startInConversation = startInConversation
	app.runCtx = ctx
	app.runCancel = cancel
	app.wg = &sync.WaitGroup{}
	app.progress = newEventBridge(16)
	app.slot = newConversationSlot()
	app.meetingSlot = newMeetingSlot()

	// Native libraries write directly to file descriptor 2, bypassing Bubble
	// Tea and corrupting the terminal surface. Keep those diagnostics in a log;
	// debug-audio runs colocate it with the capture bundle.
	//
	// Trade-off: restoreDiagnostics only runs via this goroutine's defers, so
	// an unrecovered panic on a different goroutine (a pipeline or TTS worker,
	// for instance) still crashes with fd 2 pointed at the log file — its
	// stack trace lands in native-diagnostics.log instead of the terminal.
	restoreDiagnostics, err := redirectNativeDiagnostics(DiagnosticsLogPath())
	if err != nil {
		return false, fmt.Errorf("redirect native diagnostics: %w", err)
	}
	defer func() { _ = restoreDiagnostics() }()

	// Force dark + truecolor before Bubble Tea can issue OSC queries that
	// hang or mis-detect on bare PTYs (VHS/ttyd). Same pattern as festival.
	forceTUIColorProfile()

	// The mouse is left to the terminal by default (tui_mouse_enabled=false),
	// so click-drag selection and copy work the way they do in any other agent
	// chat. A transcript exists to be read and copied out of; taking selection
	// away to gain wheel routing is the wrong side of that trade.
	//
	// Nothing is lost to get it. Page Up / Page Down always scroll, and with no
	// mouse claim a terminal in the alternate screen sends arrow keys for wheel
	// turns, which the conversation scrolls with while the composer is empty.
	//
	// tui_mouse_enabled=true opts back in to cell-motion reporting (wheel and
	// trackpad delivered straight to the viewport, pointer movement reported
	// only while a button is held) at the cost of needing a modifier to select
	// — option in iTerm2, fn in Terminal.app, shift in kitty.
	//
	// The claim is not a startup option because only the conversation routes
	// wheel events — App.Update takes it on entering that screen and releases
	// it on leaving, so the launcher, settings and meeting screens are
	// unaffected either way.
	m, runErr := program(app)
	final, _ := m.(App)

	// Flush the chat before bounding remaining teardown so a wedged TTS, STT,
	// or talk-helper close cannot discard it. Prefer the slot: it still holds
	// a runtime if build finished after quit and the ready message never
	// reached Update.
	if final.slot != nil {
		final.slot.persist()
	} else if app.slot != nil {
		app.slot.persist()
	}
	// Meeting recorder may still be open if the program quit mid-session
	// without a meetingDoneMsg (e.g. outer Quit). Idempotent when already
	// closed. This writes the bundle trailer that a bare Cleanup does not.
	if err := final.stopMeetingRuntime(); err != nil {
		final.fatalErr = errors.Join(final.fatalErr, err)
	}

	// Returning to the menu must finish cleanup so the next launcher does not
	// inherit a hot mic or loaded model. Process quit matches the CLI: a
	// blocked shutdown cannot leave the process ignoring the user's exit.
	disarm := func() {}
	if !final.returnToMenu {
		disarm = armProcessForceQuit()
	}
	defer disarm()

	waitQuitHelpers(final, app, cancel)

	if final.slot != nil {
		final.slot.cleanup()
	} else if app.slot != nil {
		app.slot.cleanup()
	}

	// Then the slot, for the recorder that finished building after the quit
	// and never reached meetingRT. Cleanup is idempotent, so a recorder the
	// line above already stopped is unaffected.
	if final.meetingSlot != nil {
		final.meetingSlot.cleanup()
	} else if app.meetingSlot != nil {
		app.meetingSlot.cleanup()
	}

	if runErr != nil {
		return false, fmt.Errorf("TUI error: %w", runErr)
	}
	return final.returnToMenu, final.fatalErr
}

func armProcessForceQuit() func() {
	// Read the package-level knobs here, on the caller's goroutine, so tests
	// that override them for one run never race the timer goroutine of the
	// previous run, which may not have started yet when the next test writes.
	after, exit := processForceQuitAfter, processForceExit
	done := make(chan struct{})
	var once sync.Once
	go func() {
		timer := time.NewTimer(after)
		defer timer.Stop()
		select {
		case <-timer.C:
			exit(130)
		case <-done:
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}

// waitQuitHelpers asks remote serve, the talk helper, and in-flight turns to
// leave, then waits once. Sequential stop timeouts used to stack on every
// quit (remote + hotkey + drain).
func waitQuitHelpers(final, app App, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if final.remote.server != nil {
			final.remote.stopAndWait(remoteStopTimeout)
		} else {
			app.remote.stopAndWait(remoteStopTimeout)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		// The talk-key helper holds a system-wide key; it must not outlive
		// the conversation that owned it. Quit only asked it to leave —
		// waiting is this side's job. final is the model the program ran;
		// app is the pre-run copy and cleans up nothing here, but it covers
		// the day App becomes a pointer model.
		final.stopTalkHotkey()
		app.stopTalkHotkey()
	}()
	if app.wg != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.wg.Wait()
		}()
	}
	waitTimeout(&wg, drainTimeout)
}
