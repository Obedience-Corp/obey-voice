package brain

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

func TestPersonaDirectoryRealToolsRemainIsolated(t *testing.T) {
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	cwd, _ := os.Getwd()
	var wg sync.WaitGroup
	for _, name := range []string{"alpha", "beta"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "identity.txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := config.ResolveSessionDirectory(&config.Config{}, "", root, "", "")
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{WorkDir: info.Dir, WorkDirSource: string(info.Source), VoiceWorkerBoundary: true}
		if config.WorkerBoundaryFor(cfg) != info.Dir {
			t.Fatal("worker boundary disagrees")
		}
		paths := skills.DefaultSearchPaths(config.WorkDirFrom(cfg), "")
		if len(paths) == 0 || paths[0] != filepath.Join(info.Dir, ".agents", "skills") {
			t.Fatal(paths)
		}
		wg.Go(func() {
			dir := config.WorkDirFrom(cfg)
			read := toolReadFile(dir, map[string]any{"path": "identity.txt"})
			if !strings.Contains(read, name) {
				t.Errorf("relative read in %s: %s", dir, read)
			}
			out := toolRunCommandWithTimeout(t.Context(), dir, map[string]any{"command": "pwd; cat identity.txt"}, 5*time.Second)
			if !strings.Contains(out, dir) || !strings.Contains(out, name) {
				t.Errorf("shell in %s: %s", dir, out)
			}
			if !strings.Contains(EnvironmentContext(dir), dir) {
				t.Error("prompt directory disagrees")
			}
		})
	}
	wg.Wait()
	if after, _ := os.Getwd(); after != cwd {
		t.Fatal("process cwd changed")
	}
}
