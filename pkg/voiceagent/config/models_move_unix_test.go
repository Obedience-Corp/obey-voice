//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var errCrossDevice error = syscall.EXDEV

// The old copy silently skipped anything that was not a file, folder or link,
// then deleted the old folder with it inside.
func TestMoveModelsRefusesFilesItCannotCopy(t *testing.T) {
	cfg, source, root := moveFixture(t)
	fifo := filepath.Join(source, "worker.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
	_, err := MoveModels(context.Background(), cfg, filepath.Join(root, "fast", "models"), copyOpts(t, nil))
	if err == nil || !strings.Contains(err.Error(), "worker.pipe") {
		t.Fatalf("err = %v, want it to name worker.pipe", err)
	}
	if info, err := os.Lstat(fifo); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the FIFO is gone: %v", err)
	}
	assertNothingMoved(t, cfg, source, filepath.Join(root, "fast", "models"))
}
