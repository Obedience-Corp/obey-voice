//go:build vhs

package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLauncherNoticeVHS runs demos/launcher-notice.tape against a built
// binary. Default go test does not include this tag, so CI does not rewrite
// the tracked gif. Record with: go test -tags vhs ./internal/tui/ -run TestLauncherNoticeVHS -count=1 -timeout 10m
func TestLauncherNoticeVHS(t *testing.T) {
	if _, err := exec.LookPath("vhs"); err != nil {
		t.Skip("vhs not installed")
	}
	root := moduleRoot(t)
	t.Chdir(root)

	bin := filepath.Join(root, "bin", "obey-voice")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/obey-voice")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(root, "demos", "frames"), 0o755); err != nil {
		t.Fatal(err)
	}

	vhs := exec.Command("vhs", "demos/launcher-notice.tape")
	vhs.Env = withoutEnv(os.Environ(), "NO_COLOR", "CLICOLOR")
	vhs.Env = append(vhs.Env,
		"CLICOLOR_FORCE=1",
		"FORCE_COLOR=1",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	out, err := vhs.CombinedOutput()
	if err != nil {
		t.Fatalf("vhs: %v\n%s", err, out)
	}

	collapsed := mustRead(t, filepath.Join(root, "demos", "frames", "launcher-notice.png"))
	opened := mustRead(t, filepath.Join(root, "demos", "frames", "launcher-notice-open.png"))
	if len(collapsed) < 1000 || len(opened) < 1000 {
		t.Fatalf("frames too small: collapsed=%d open=%d", len(collapsed), len(opened))
	}
	if bytes.Equal(collapsed, opened) {
		t.Fatal("expanded notices frame matches the collapsed rail")
	}
	gif, err := os.Stat(filepath.Join(root, "demos", "launcher-notice.gif"))
	if err != nil {
		t.Fatal(err)
	}
	if gif.Size() < 1000 {
		t.Fatalf("gif size = %d", gif.Size())
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withoutEnv(env []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	var out []string
	for _, entry := range env {
		name, _, _ := bytes.Cut([]byte(entry), []byte("="))
		if drop[string(name)] {
			continue
		}
		out = append(out, entry)
	}
	return out
}
