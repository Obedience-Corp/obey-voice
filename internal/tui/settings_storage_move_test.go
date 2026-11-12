package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func noPersonas(*config.Config) ([]config.PersonaAssets, error) { return nil, nil }

// heldMove is a fake move the test steps through: it reports the phases it is
// given, then waits for the test to say how it ended. With stopOnCancel it
// ends as soon as it is cancelled, as a real move before its save does.
type heldMove struct {
	phases       chan config.MoveProgress
	finish       chan error
	ctx          chan context.Context
	stopOnCancel bool
}

func newHeldMove() *heldMove {
	return &heldMove{phases: make(chan config.MoveProgress, 4), finish: make(chan error, 1), ctx: make(chan context.Context, 1)}
}

func (h *heldMove) run(ctx context.Context, _ *config.Config, dest string, opts config.MoveOptions) (config.MoveResult, error) {
	h.ctx <- ctx
	cancelled := ctx.Done()
	if !h.stopOnCancel {
		cancelled = nil
	}
	for {
		select {
		case p := <-h.phases:
			opts.Progress(p)
		case <-cancelled:
			return config.MoveResult{}, ctx.Err()
		case err := <-h.finish:
			if err != nil {
				return config.MoveResult{}, err
			}
			return config.MoveResult{From: "/old", To: dest, Bytes: 1 << 20, Files: 1}, nil
		}
	}
}

// startHeld starts a move to dest and runs its worker in the background,
// returning the event pump, the hang-up watcher and the worker's message.
func startHeld(t *testing.T, m *settingsModel, h *heldMove, dest string) (pump, hangup tea.Cmd, done <-chan tea.Msg) {
	t.Helper()
	m.storage.runMove, m.storage.personas = h.run, noPersonas
	batch, ok := m.startStorageMove(dest)().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Fatalf("start returned %T", batch)
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- batch[2]() }()
	select {
	case <-h.ctx:
	case <-time.After(5 * time.Second):
		t.Fatal("the move worker never started")
	}
	return batch[0], batch[1], out
}

func recv(t *testing.T, ch <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("no message from the move worker")
		return nil
	}
}

func TestStorageMoveIsCheckedAndConfirmedBeforeItStarts(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	m.storage.personas = noPersonas
	m.storage.runMove = func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error) {
		t.Fatal("moved without a yes")
		return config.MoveResult{}, nil
	}
	m.storage.plan = func(_ context.Context, _ *config.Config, dest string, _ config.MoveOptions) (config.MovePlan, error) {
		return config.MovePlan{From: "/old", To: dest, Measured: true, Bytes: 21 << 30, Files: 412,
			Unrecognized: []config.MoveEntry{{Name: "lmstudio", Bytes: 3 << 30}}}, nil
	}
	m.section, m.width, m.height = sectionStorage, 120, 40
	check := m.armStorageMove(" /fast ")
	if m.storageConfirming() || m.message != "Checking the move…" {
		t.Fatalf("confirmed before the check: %q", m.message)
	}
	m, _ = m.Update(check())
	view := m.View()
	for _, want := range []string{"Confirm move", "Move 21.0 GiB in 412 files?", "/old", "/fast", "lmstudio", "3.0 GiB", "old folder is then kept"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the confirmation is missing %q:\n%s", want, view)
		}
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd != nil || m.storage.moving() || !strings.Contains(m.message, "Kept the models in /old") {
		t.Fatalf("a no started something: moving=%v message=%q", m.storage.moving(), m.message)
	}

	m.storage.plan = func(context.Context, *config.Config, string, config.MoveOptions) (config.MovePlan, error) {
		return config.MovePlan{Refusals: []string{"/fast already has files in it"}}, nil
	}
	m, _ = m.Update(m.armStorageMove("/fast")())
	if m.storageConfirming() || !strings.Contains(m.message, "can't be moved: /fast already has files") {
		t.Fatalf("a refused move was offered: %q", m.message)
	}
}

// Cancel used to say the models were still in the old folder at once, and
// re-enable Move, while the worker was still running.
func TestStorageCancelKeepsMoveOffUntilTheWorkerEnds(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	h := newHeldMove()
	_, _, done := startHeld(t, &m, h, "/fast")

	storageRow(t, m, "storage_cancel").run(&m)
	if m.message != "Cancelling the move…" {
		t.Fatalf("cancel message = %q", m.message)
	}
	if row := storageRow(t, m, "storage_move"); !row.disabled {
		t.Fatal("Move was enabled again while the first move was still stopping")
	}
	if cmd := m.startStorageMove("/elsewhere"); cmd != nil {
		t.Fatal("a second move started while the first was still stopping")
	}
	if row := storageRow(t, m, "storage_cancel"); !row.disabled || row.name != "Cancelling move…" {
		t.Fatalf("cancel row = %+v", row)
	}

	h.finish <- context.Canceled
	m, _ = m.Update(recv(t, done))
	if m.storage.moving() || storageRow(t, m, "storage_move").disabled || m.message != "Move cancelled; the models are still in /old" {
		t.Fatalf("after the worker ended: moving=%v message=%q", m.storage.moving(), m.message)
	}
}

// Once the new folder is saved a cancel cannot undo the move; the screen must
// say so rather than claim the models stayed put.
func TestStorageCancelAfterTheSaveSaysTheMoveWillFinish(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	h := newHeldMove()
	pump, _, done := startHeld(t, &m, h, "/fast")
	h.phases <- config.MoveProgress{Phase: config.MovePhaseSave}
	m, _ = m.Update(pump())

	if row := storageRow(t, m, "storage_cancel"); !row.disabled {
		t.Fatal("Cancel is offered after the new folder was saved")
	}
	m.cancelStorageMove()
	if !strings.Contains(m.message, "already saved, so the move will finish") || strings.Contains(m.message, "still in /old") {
		t.Fatalf("cancel message = %q", m.message)
	}
	h.finish <- nil
	m, _ = m.Update(recv(t, done))
	if cfg.ModelsDir != "/fast" || !strings.Contains(m.message, "Moved 1.0 MiB to /fast") {
		t.Fatalf("models_dir=%q message=%q", cfg.ModelsDir, m.message)
	}
}

// A cancel can land after the worker's last cancel check. The phases it then
// goes through must still reach the screen, not a frozen "Cancelling".
func TestStorageCancelThatArrivesTooLateShowsTheMoveFinishing(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	h := newHeldMove()
	pump, _, done := startHeld(t, &m, h, "/fast")
	m.cancelStorageMove()
	if m.message != "Cancelling the move…" {
		t.Fatalf("cancel message = %q", m.message)
	}
	h.phases <- config.MoveProgress{Phase: config.MovePhaseSave}
	pumped := make(chan tea.Msg, 1)
	go func() { pumped <- pump() }()
	m, _ = m.Update(recv(t, pumped))
	if !strings.Contains(m.message, "already saved, so the move will finish") {
		t.Fatalf("after a committed phase the message is %q", m.message)
	}
	h.finish <- nil
	m, _ = m.Update(recv(t, done))
	if cfg.ModelsDir != "/fast" || !strings.Contains(m.message, "already saved, so the move finished: Moved 1.0 MiB to /fast") {
		t.Fatalf("models_dir=%q message=%q", cfg.ModelsDir, m.message)
	}
}

// Progress messages can be dropped when the screen falls behind. Whether
// Cancel can still undo the move must come from the worker, not from them.
func TestTheMoveScreenFollowsTheWorkerWhenAProgressMessageIsLost(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	h := newHeldMove()
	_, _, done := startHeld(t, &m, h, "/fast")
	h.phases <- config.MoveProgress{Phase: config.MovePhaseSave}
	deadline := time.Now().Add(5 * time.Second)
	for m.storage.job.currentPhase() != config.MovePhaseSave {
		if time.Now().After(deadline) {
			t.Fatal("the worker never reported the save")
		}
		time.Sleep(time.Millisecond)
	}
	// The progress message is never delivered.
	if row := storageRow(t, m, "storage_cancel"); !row.disabled {
		t.Fatal("Cancel is offered after the save because a message was lost")
	}
	m.cancelStorageMove()
	if !strings.Contains(m.message, "already saved") {
		t.Fatalf("cancel message = %q", m.message)
	}
	h.finish <- nil
	recv(t, done)
}

// Picking a speaker used to stop every preview and, with them, a running move.
func TestChangingTheSpeakerDoesNotCancelAMove(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	m.saveConfig = func(string, any) error { return nil }
	h := newHeldMove()
	_, _, done := startHeld(t, &m, h, "/fast")
	ctx := m.storage.job.ctx
	m.selectOutputDevice("USB speakers")
	if ctx.Err() != nil || !m.storage.moving() {
		t.Fatalf("changing the speaker cancelled the move: %v", ctx.Err())
	}
	h.finish <- nil
	m, _ = m.Update(recv(t, done))
	if m.cfg.ModelsDir != "/fast" {
		t.Fatalf("models_dir = %q", m.cfg.ModelsDir)
	}
}

func storageTestApp(t *testing.T) App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("MODELS_DIR", "")
	config.SetConfigDirForTest(t, filepath.Join(root, "install"))
	app := newApp(&config.Config{ModelsDir: "/old", BrainProvider: "ollama"}, nil, nil)
	model, _ := app.onSwitchScreen(switchScreenMsg(screenSettings))
	return model.(App)
}

// A move that finished after Settings was left used to reach nothing, leaving
// the App on the old folder.
func TestAppAppliesAMoveThatFinishesAfterSettingsWasLeft(t *testing.T) {
	app := storageTestApp(t)
	h := newHeldMove()
	_, _, done := startHeld(t, &app.settings, h, "/fast")
	ctx := app.settings.storage.job.ctx
	model, _ := app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)
	if ctx.Err() == nil {
		t.Fatal("leaving Settings did not cancel the move")
	}
	if !app.settings.storage.moving() {
		t.Fatal("leaving Settings forgot the running move")
	}

	// Re-entering Settings shows the same move, still finishing.
	model, _ = app.onSwitchScreen(switchScreenMsg(screenSettings))
	app = model.(App)
	if row := storageRow(t, app.settings, "storage_move"); !row.disabled {
		t.Fatal("Move is offered while the earlier move is still finishing")
	}
	model, _ = app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)

	// The save had already happened, so the move finishes.
	h.finish <- nil
	model, _ = app.Update(recv(t, done))
	app = model.(App)
	if app.cfg.ModelsDir != "/fast" {
		t.Fatalf("app models_dir = %q, want the new folder", app.cfg.ModelsDir)
	}
	if !strings.Contains(app.launcher.banner, "Moved 1.0 MiB to /fast") {
		t.Fatalf("launcher banner = %q", app.launcher.banner)
	}
}

// Leaving Settings with esc cancels the move, as the confirmation says.
func TestLeavingSettingsWithEscCancelsTheMove(t *testing.T) {
	app := storageTestApp(t)
	h := newHeldMove()
	_, _, done := startHeld(t, &app.settings, h, "/fast")
	ctx := app.settings.storage.job.ctx
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc did not leave Settings")
	}
	model, _ = model.(App).Update(cmd())
	app = model.(App)
	if app.screen == screenSettings || ctx.Err() == nil {
		t.Fatalf("after esc: screen=%v, move context=%v", app.screen, ctx.Err())
	}
	h.finish <- context.Canceled
	model, _ = app.Update(recv(t, done))
	if banner := model.(App).launcher.banner; banner != "Move cancelled; the models are still in /old" {
		t.Fatalf("launcher banner = %q", banner)
	}
}

// A failed move that is not the current one used to put the last progress
// line on the launcher as an error.
func TestAStaleMoveResultLeavesNoBanner(t *testing.T) {
	app := storageTestApp(t)
	model, _ := app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)
	app.settings.message = "Moving models: copying: 1.0 GiB of 4.0 GiB (25%)"
	model, _ = app.Update(storageMoveDoneMsg{seq: -1, err: errors.New("disk full")})
	if banner := model.(App).launcher.banner; banner != "" {
		t.Fatalf("launcher banner = %q", banner)
	}
}

// Nothing that reads models may start while they are being moved.
func TestAppHoldsConversationsAndMeetingsWhileModelsMove(t *testing.T) {
	app := storageTestApp(t)
	h := newHeldMove()
	_, _, done := startHeld(t, &app.settings, h, "/fast")
	model, _ := app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)

	for name, msg := range map[string]tea.Msg{"conversation": startPipelineMsg{}, "meeting": startMeetingMsg{Description: "standup"}} {
		model, _ := app.Update(msg)
		next := model.(App)
		if next.screen != screenLauncher || next.launcher.banner != modelsMoveBusy {
			t.Fatalf("%s started during a move: screen=%v banner=%q", name, next.screen, next.launcher.banner)
		}
	}
	h.finish <- context.Canceled
	_, _ = app.Update(recv(t, done))
}

// However the program ends, a running move is cancelled and its cleanup is
// waited for before teardown goes on.
func TestEndingTheProgramCancelsTheMoveAndWaitsForIt(t *testing.T) {
	t.Setenv("MODELS_DIR", "")
	origSay := storageWaitSay
	storageWaitSay = func(string) {}
	t.Cleanup(func() { storageWaitSay = origSay })
	for name, end := range map[string]func(App) App{
		// Bubble Tea's SIGTERM and non-terminal SIGINT end the program
		// without the App's quit handler.
		"by signal": func(app App) App { return app },
		"by quit key": func(app App) App {
			model, _ := app.Update(quitMsg{})
			return model.(App)
		},
	} {
		t.Run(name, func(t *testing.T) {
			config.SetConfigDirForTest(t, t.TempDir())
			h := newHeldMove()
			h.stopOnCancel = true
			var moveCtx context.Context
			var workerDone <-chan tea.Msg
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_ = runPrograms(&config.Config{ModelsDir: "/old"}, nil, nil, false, func(app App) (tea.Model, error) {
					_, _, workerDone = startHeld(t, &app.settings, h, "/fast")
					moveCtx = app.settings.storage.job.ctx
					return end(app), nil
				})
			}()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("the program never finished")
			}
			if moveCtx.Err() == nil {
				t.Fatal("the move was still running when the program had ended")
			}
			select {
			case msg := <-workerDone:
				if done, ok := msg.(storageMoveDoneMsg); !ok || !errors.Is(done.err, context.Canceled) {
					t.Fatalf("worker ended with %+v", msg)
				}
			default:
				t.Fatal("teardown went on before the move's worker had ended")
			}
		})
	}
}

// A move whose worker never started, because the program ended first, must
// not hold up teardown.
func TestEndingTheProgramBeforeTheMoveStartsDoesNotWait(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	origSay := storageWaitSay
	storageWaitSay = func(string) {}
	t.Cleanup(func() { storageWaitSay = origSay })
	finished := make(chan struct{})
	ran := false
	go func() {
		defer close(finished)
		_ = runPrograms(&config.Config{ModelsDir: "/old"}, nil, nil, false, func(app App) (tea.Model, error) {
			app.settings.storage.personas = noPersonas
			app.settings.storage.runMove = func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error) {
				ran = true
				return config.MoveResult{}, nil
			}
			batch := app.settings.startStorageMove("/fast")().(tea.BatchMsg)
			t.Cleanup(func() { batch[2]() })
			return app, nil
		})
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("teardown waited for a move that never started")
	}
	if ran {
		t.Fatal("an abandoned move ran")
	}
}
