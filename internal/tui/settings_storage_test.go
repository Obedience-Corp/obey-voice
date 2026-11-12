package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func storageRow(t *testing.T, m settingsModel, key string) settingRow {
	t.Helper()
	for _, row := range m.storageRows() {
		if row.key == key {
			return row
		}
	}
	t.Fatalf("no %s row in %v", key, m.storageRows())
	return settingRow{}
}

func TestStorageSectionIsReachableByItsDigitAndShowsTheFolder(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/models/here"}
	m := newSettings(cfg, nil)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("0")})
	if m.section != sectionStorage {
		t.Fatalf("0 opened section %d, want Storage", m.section)
	}
	m.width, m.height = 100, 30
	view := m.View()
	for _, want := range []string{"0 Storage", "Models folder", "/models/here", "Size on disk", "not measured", "Move models to another folder"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Storage view is missing %q:\n%s", want, view)
		}
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if m.section != sectionBrain {
		t.Fatalf("1 opened section %d, want Brain", m.section)
	}
}

func TestStorageMeasuresOffTheUIThreadAndIgnoresStaleResults(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/models/here"}, nil)
	m.storage.measure = func(_ context.Context, dir string) (int64, int, error) {
		if dir != "/models/here" {
			t.Fatalf("measured %q", dir)
		}
		return 3 << 30, 12, nil
	}
	cmd := m.startStorageUsage()
	if got := storageRow(t, m, "storage_usage"); got.value != "measuring…" || !got.disabled {
		t.Fatalf("while measuring: %+v", got)
	}
	stale := storageUsageMsg{seq: m.storage.seq - 1, bytes: 1, files: 1}
	m, _ = m.Update(stale)
	if !m.storage.measuring {
		t.Fatal("a stale result ended the measurement")
	}
	m, _ = m.Update(cmd())
	if got := storageRow(t, m, "storage_usage").value; got != "3.0 GiB in 12 files" {
		t.Fatalf("size = %q", got)
	}
}

func TestStorageMoveReportsProgressThenUsesTheNewFolder(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	release := make(chan struct{})
	m.storage.runMove = func(_ context.Context, c *config.Config, dest string, opts config.MoveOptions) (config.MoveResult, error) {
		if c == cfg {
			t.Fatal("the move must not mutate the live config off the UI thread")
		}
		opts.Progress(config.MoveProgress{Phase: config.MovePhaseCopy, Done: 1 << 30, Total: 4 << 30})
		<-release
		return config.MoveResult{From: "/old", To: dest, Bytes: 4 << 30, Files: 7, Copied: true, RewrittenKeys: []string{"whispercpp_model_path"}}, nil
	}
	m.speechSetup.running = true
	if row := storageRow(t, m, "storage_move"); !row.disabled || !strings.Contains(row.reason, "download") {
		t.Fatalf("a move must wait for a model download: %+v", row)
	}
	m.speechSetup.running = false
	m.section = sectionStorage
	storageRow(t, m, "storage_move").run(&m)
	if m.edit.kind != editStorageMove {
		t.Fatal("Move did not open the folder editor")
	}
	m.edit.input.SetValue("  /fast/models ")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.storage.moving() || m.storage.confirmTo != "/fast/models" {
		t.Fatalf("the move started before it was confirmed: confirm=%q", m.storage.confirmTo)
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	batch := cmd().(tea.BatchMsg)
	if !m.storage.moving() || !storageRow(t, m, "models_dir").disabled {
		t.Fatal("the folder row must be locked during a move")
	}
	storageRow(t, m, "storage_cancel")
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[1]() }()
	m, _ = m.Update(batch[0]())
	if !strings.Contains(m.message, "1.0 GiB of 4.0 GiB (25%)") {
		t.Fatalf("progress message = %q", m.message)
	}
	close(release)
	m, _ = m.Update(<-done)
	if m.storage.moving() || cfg.ModelsDir != "/fast/models" {
		t.Fatalf("after the move: moving=%v models_dir=%q", m.storage.moving(), cfg.ModelsDir)
	}
	if !strings.Contains(m.message, "Moved 4.0 GiB to /fast/models") || !strings.Contains(m.message, "whispercpp_model_path") {
		t.Fatalf("done message = %q", m.message)
	}
	if got := storageRow(t, m, "storage_usage").value; got != "4.0 GiB in 7 files" {
		t.Fatalf("size after move = %q", got)
	}
}

func TestStorageMoveFailureAndCancelKeepTheCurrentFolder(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	m.storage.runMove = func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error) {
		return config.MoveResult{}, errors.New("/fast already has files in it")
	}
	batch := m.startStorageMove("/fast")().(tea.BatchMsg)
	m, _ = m.Update(batch[1]())
	if cfg.ModelsDir != "/old" || !strings.Contains(m.message, "Models were not moved: /fast already has files") {
		t.Fatalf("models_dir=%q message=%q", cfg.ModelsDir, m.message)
	}

	m.storage.runMove = func(ctx context.Context, _ *config.Config, _ string, _ config.MoveOptions) (config.MoveResult, error) {
		<-ctx.Done()
		return config.MoveResult{}, ctx.Err()
	}
	batch = m.startStorageMove("/fast")().(tea.BatchMsg)
	seq := m.storage.job.seq
	storageRow(t, m, "storage_cancel").run(&m)
	late := batch[1]().(storageMoveDoneMsg)
	if late.seq != seq || !errors.Is(late.err, context.Canceled) {
		t.Fatalf("cancel did not reach the move: %+v", late)
	}
	m, _ = m.Update(late)
	if m.storage.moving() || cfg.ModelsDir != "/old" || !strings.Contains(m.message, "still in /old") {
		t.Fatalf("after cancel: moving=%v models_dir=%q message=%q", m.storage.moving(), cfg.ModelsDir, m.message)
	}
}

func TestStorageMoveIsRefusedDuringAConversation(t *testing.T) {
	m := newSettings(&config.Config{ModelsDir: "/old"}, nil)
	m.modelsInUse = true
	m.storage.runMove = func(context.Context, *config.Config, string, config.MoveOptions) (config.MoveResult, error) {
		t.Fatal("moved models out from under a running conversation")
		return config.MoveResult{}, nil
	}
	row := storageRow(t, m, "storage_move")
	if !row.disabled || !strings.Contains(row.reason, "End the conversation or meeting") {
		t.Fatalf("row = %+v", row)
	}
	if cmd := m.startStorageMove("/fast"); cmd != nil || m.storage.moving() {
		t.Fatal("a move started during a conversation")
	}
}

// A move that completes after Cancel was pressed still moved the files; the
// screen must follow them rather than keep naming a folder that is gone.
func TestStorageMoveThatFinishesAfterCancelStillApplies(t *testing.T) {
	cfg := &config.Config{ModelsDir: "/old"}
	m := newSettings(cfg, nil)
	release := make(chan struct{})
	m.storage.runMove = func(_ context.Context, _ *config.Config, dest string, _ config.MoveOptions) (config.MoveResult, error) {
		<-release
		return config.MoveResult{From: "/old", To: dest, Bytes: 1 << 20, Files: 1}, nil
	}
	batch := m.startStorageMove("/fast")().(tea.BatchMsg)
	storageRow(t, m, "storage_cancel").run(&m)
	close(release)
	m, _ = m.Update(batch[1]())
	if cfg.ModelsDir != "/fast" || !strings.Contains(m.message, "already saved, so the move finished: Moved 1.0 MiB to /fast") {
		t.Fatalf("models_dir=%q message=%q", cfg.ModelsDir, m.message)
	}
}
