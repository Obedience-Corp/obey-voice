package tui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// heldMove is a fake move the test steps through: it reports the phases it is
// given, then waits for the test to say how it ended.
type heldMove struct {
	phases chan config.MoveProgress
	finish chan error
	ctx    chan context.Context
}

func newHeldMove() *heldMove {
	return &heldMove{phases: make(chan config.MoveProgress, 4), finish: make(chan error, 1), ctx: make(chan context.Context, 1)}
}

func (h *heldMove) run(ctx context.Context, _ *config.Config, dest string, opts config.MoveOptions) (config.MoveResult, error) {
	h.ctx <- ctx
	for {
		select {
		case p := <-h.phases:
			opts.Progress(p)
		case err := <-h.finish:
			if err != nil {
				return config.MoveResult{}, err
			}
			return config.MoveResult{From: "/old", To: dest, Bytes: 1 << 20, Files: 1}, nil
		}
	}
}

// startHeld starts a move to dest and runs its worker in the background,
// returning the event pump and the worker's eventual message.
func startHeld(t *testing.T, m *settingsModel, h *heldMove, dest string) (pump tea.Cmd, done <-chan tea.Msg) {
	t.Helper()
	m.storage.runMove = h.run
	batch, ok := m.startStorageMove(dest)().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("start returned %T", batch)
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- batch[1]() }()
	select {
	case <-h.ctx:
	case <-time.After(5 * time.Second):
		t.Fatal("the move worker never started")
	}
	return batch[0], out
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

func TestStorageMoveAsksBeforeItStarts(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	m.storage.runMove = func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error) {
		t.Fatal("moved without a yes")
		return config.MoveResult{}, nil
	}
	m.section, m.width, m.height = sectionStorage, 100, 30
	m.armStorageMove(" /fast ")
	if view := m.View(); !strings.Contains(view, "Confirm move") || !strings.Contains(view, "/fast") || !strings.Contains(view, "/old") {
		t.Fatalf("no confirmation naming both folders:\n%s", view)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cmd != nil || m.storage.moving() || !strings.Contains(m.message, "Kept the models in /old") {
		t.Fatalf("a no started something: moving=%v message=%q", m.storage.moving(), m.message)
	}
}

// Cancel used to say the models were still in the old folder at once, and
// re-enable Move, while the worker was still running.
func TestStorageCancelKeepsMoveOffUntilTheWorkerEnds(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	h := newHeldMove()
	_, done := startHeld(t, &m, h, "/fast")

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
	pump, done := startHeld(t, &m, h, "/fast")
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
	pump, done := startHeld(t, &m, h, "/fast")
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

// Picking a speaker used to stop every preview and, with them, a running move.
func TestChangingTheSpeakerDoesNotCancelAMove(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	m.saveConfig = func(string, any) error { return nil }
	h := newHeldMove()
	_, done := startHeld(t, &m, h, "/fast")
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
	_, done := startHeld(t, &app.settings, h, "/fast")
	model, _ := app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)
	if !app.settings.storage.moving() {
		t.Fatal("leaving Settings forgot the running move")
	}

	// Re-entering Settings shows the same move, still running.
	model, _ = app.onSwitchScreen(switchScreenMsg(screenSettings))
	app = model.(App)
	if row := storageRow(t, app.settings, "storage_move"); !row.disabled {
		t.Fatal("Move is offered while the earlier move is still finishing")
	}
	model, _ = app.onSwitchScreen(switchScreenMsg(screenLauncher))
	app = model.(App)

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
	_, done := startHeld(t, &app.settings, h, "/fast")
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

// Nothing that reads models may start while they are being moved.
func TestAppHoldsConversationsAndMeetingsWhileModelsMove(t *testing.T) {
	app := storageTestApp(t)
	h := newHeldMove()
	_, done := startHeld(t, &app.settings, h, "/fast")
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

// Quitting cancels the move and the program's teardown waits for its cleanup.
func TestQuitWaitsForTheMoveWorker(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	var wg sync.WaitGroup
	m.storage.lifetime, m.storage.wg = context.Background(), &wg
	h := newHeldMove()
	_, done := startHeld(t, &m, h, "/fast")
	m.storage.stop()
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("teardown would not wait for the move worker")
	case <-time.After(50 * time.Millisecond):
	}
	h.finish <- context.Canceled
	recv(t, done)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never released teardown")
	}
}
