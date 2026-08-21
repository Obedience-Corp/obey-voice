package brain

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// The grounding block must name the directory the *config* selects, not the
// process cwd: a library embedder points the agent at a workspace by setting
// work_dir, never by chdir'ing its host.
func TestConfiguredWorkDirReachesGroundingBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := policyCfg()
	cfg.WorkDir = dir

	tests := []struct {
		name string
		got  string
	}{
		{"claude", (&Brain{cfg: cfg}).assembleSystem("PERSONA")},
		{"grok", (&GrokBrain{cfg: cfg}).assembleSystem("PERSONA")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.got, "Working directory: "+dir) {
				t.Errorf("assembled prompt does not carry the configured work dir %q:\n%s", dir, tt.got)
			}
		})
	}
}

// Telling a provider about the directory in prose is not enough: Claude and
// Grok run their own tools in a subprocess, so it has to arrive as a real cwd
// or every Read/Bash lands wherever this process happens to be.
func TestConfiguredWorkDirReachesProviderSubprocess(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{WorkDir: dir}
	grokOpts := (&GrokBrain{cfg: cfg}).runOptions(grok.PlainOutput, true)

	tests := []struct {
		name string
		got  string
		args []string
	}{
		{
			name: "claude",
			got:  (&Brain{cfg: cfg}).runOptions(claude.StreamJSONOutput, true).WorkingDirectory,
		},
		{
			name: "grok",
			got:  grokOpts.WorkingDirectory,
			// Grok also passes it to the CLI as --cwd; assert the argument so a
			// future SDK change cannot quietly drop it.
			args: grok.BuildArgs("prompt", grokOpts),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != dir {
				t.Errorf("WorkingDirectory = %q, want %q", tt.got, dir)
			}
			if tt.args != nil && !argPair(tt.args, "--cwd", dir) {
				t.Errorf("args = %#v, want --cwd %s", tt.args, dir)
			}
		})
	}
}

// Ollama runs tools in-process through tools.go, so its brain carries the
// directory itself.
func TestOllamaBrainCarriesConfiguredWorkDir(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"name":"test-model"}]}`)
	}))
	t.Cleanup(srv.Close)

	b, err := NewOllama(&config.Config{
		OllamaModel: "test-model",
		OllamaHost:  srv.URL,
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	if b.workDir != dir {
		t.Errorf("OllamaBrain.workDir = %q, want %q", b.workDir, dir)
	}
}

// The shell tool must actually run in the directory it is handed, or every
// layer above it is decoration.
func TestRunCommandExecutesInTheWorkDir(t *testing.T) {
	dir := t.TempDir()

	got := toolRunCommandWithTimeout(context.Background(), dir,
		map[string]any{"command": "pwd"}, 10*time.Second)
	if !strings.Contains(got, dir) {
		t.Errorf("pwd output = %q, want it to name %q", got, dir)
	}
}

func argPair(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
