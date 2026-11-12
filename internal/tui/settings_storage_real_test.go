//go:build !windows

package tui

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Closing the terminal sends SIGHUP, which Bubble Tea does not handle: the
// process used to die mid-copy. It now quits the program the normal way, so
// the move's cleanup runs.
func TestAClosedTerminalQuitsAndCancelsTheMove(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGHUP)
	defer signal.Stop(guard)
	app := storageTestApp(t)
	h := newHeldMove()
	_, hangup, done := startHeld(t, &app.settings, h, "/fast")
	got := make(chan tea.Msg, 1)
	go func() { got <- hangup() }()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	model, cmd := app.Update(recv(t, got))
	app = model.(App)
	if !app.quitting || cmd == nil {
		t.Fatal("a closed terminal did not quit the program")
	}
	h.finish <- context.Canceled
	recv(t, done)
}

func crossDrive(oldpath, newpath string) error {
	if filepath.Dir(oldpath) == filepath.Dir(newpath) {
		return os.Rename(oldpath, newpath)
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
}

// pumpUntilDone feeds the worker's messages to the screen until the move
// reports its end, as the program would.
func pumpUntilDone(t *testing.T, m settingsModel, pump tea.Cmd, done <-chan tea.Msg) settingsModel {
	t.Helper()
	pumped := make(chan tea.Msg, 1)
	go func() { pumped <- pump() }()
	for {
		select {
		case msg := <-pumped:
			if msg == nil {
				continue
			}
			var next tea.Cmd
			m, next = m.Update(msg)
			if next != nil {
				go func() { pumped <- next() }()
			}
		case msg := <-done:
			m, _ = m.Update(msg)
			return m
		case <-time.After(10 * time.Second):
			t.Fatal("the move never finished")
		}
	}
}

// The whole Settings flow with the real move on real folders: cancel during
// the copy, then a move that completes.
func TestStorageMovesRealFoldersAndCancelsCleanly(t *testing.T) {
	t.Setenv("MODELS_DIR", "")
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	config.SetConfigDirForTest(t, filepath.Join(root, "install"))
	source := filepath.Join(root, "models")
	for rel, body := range map[string]string{"silero_vad.onnx": "vad", "whispercpp/ggml-base.en.bin": "whisper-weights"} {
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := config.SetKeyFileValue("models_dir", source); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "fast", "models")
	pressed, resume := make(chan struct{}), make(chan struct{})
	cancelOnce := true
	config.SetModelsMoveHooksForTest(t, config.ModelsMoveTestHooks{
		Rename:    crossDrive,
		ServeRoot: filepath.Join(root, "serve-control"),
		AfterCopy: func(context.Context, string) {
			if cancelOnce {
				cancelOnce = false
				close(pressed)
				<-resume
			}
		},
	})
	cfg := &config.Config{ModelsDir: source}
	m := newSettings(cfg, nil)
	m.storage.personas = noPersonas
	batch := m.startStorageMove(dest)().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[2]() }()
	<-pressed
	m.cancelStorageMove()
	close(resume)
	m = pumpUntilDone(t, m, batch[0], done)
	if m.storage.moving() || m.message != "Move cancelled; the models are still in "+source {
		t.Fatalf("after cancel: moving=%v message=%q", m.storage.moving(), m.message)
	}
	if _, err := os.Lstat(filepath.Join(root, "fast")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cancelled copy was left: %v", err)
	}

	batch = m.startStorageMove(dest)().(tea.BatchMsg)
	done = make(chan tea.Msg, 1)
	go func() { done <- batch[2]() }()
	m = pumpUntilDone(t, m, batch[0], done)
	if cfg.ModelsDir != dest || !strings.HasPrefix(m.message, "Moved 18 B to "+dest) {
		t.Fatalf("models_dir=%q message=%q", cfg.ModelsDir, m.message)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "whispercpp", "ggml-base.en.bin")); string(got) != "whisper-weights" {
		t.Fatalf("moved file = %q", got)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old folder still present: %v", err)
	}
}
