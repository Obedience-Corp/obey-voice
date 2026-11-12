package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/fakeclaude"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// A loaded machine can take longer than the production cap just to start the
// stand-in CLI; tests that expect an answer must not depend on that cap.
func init() { probeTimeout = time.Minute }

// cliModels is fakeclaude.Models as discovery lists it.
var cliModels = []string{"default", "sonnet", "opus", "haiku", "sonnet[1m]"}

func TestClaudeDiscoveryListsTheCLIModels(t *testing.T) {
	cli := fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	workDir := t.TempDir()
	cfg := &config.Config{WorkDir: workDir, WorkDirSource: string(config.WorkDirSourceConfig)}

	info := DiscoverProvider(t.Context(), cfg, "claude")
	if !info.Available || !slices.Equal(info.Models, cliModels) {
		t.Fatalf("claude = %+v, want available with %v", info, cliModels)
	}
	want, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	// The CLI is asked from the directory a Claude brain runs in, where
	// project settings can narrow the list.
	if runs := cli.Runs(t); len(runs) != 1 || runs[0] != want {
		t.Fatalf("CLI runs = %q, want one in %s", runs, want)
	}
}

func TestClaudeDiscoveryListsTheSavedModelOnce(t *testing.T) {
	fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	for model, extra := range map[string]string{
		"":                "",
		"default":         "",
		"opus":            "",
		" OPUS ":          "",
		"Sonnet[1M]":      "",
		"claude-opus-4-1": "claude-opus-4-1",
		" my-model ":      "my-model",
	} {
		want := cliModels
		if extra != "" {
			want = append(slices.Clone(cliModels), extra)
		}
		info := DiscoverProvider(t.Context(), &config.Config{ClaudeModel: model}, "claude")
		if !slices.Equal(info.Models, want) {
			t.Errorf("claude_model %q: models = %q, want %q", model, info.Models, want)
		}
	}
}

func TestClaudeDiscoveryFallsBackWhenTheCLICannotList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   fakeclaude.Mode
		models string
	}{
		{"too old for the control protocol", fakeclaude.TooOld, ""},
		{"initialize refused", fakeclaude.Refuse, ""},
		{"no models field", fakeclaude.Answer, "null"},
		{"empty list", fakeclaude.Answer, "[]"},
		{"malformed list", fakeclaude.Answer, `{"value":"opus"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := fakeclaude.OnPath(t, tc.mode, tc.models)
			info := DiscoverProvider(t.Context(), &config.Config{ClaudeModel: "haiku"}, "claude")
			if want := []string{"default", "haiku"}; !info.Available || !slices.Equal(info.Models, want) {
				t.Fatalf("claude = %+v, want available with %v", info, want)
			}
			if runs := cli.Runs(t); len(runs) != 1 {
				t.Fatalf("CLI ran %d times, want 1", len(runs))
			}
		})
	}
}

func TestClaudeDiscoveryGivesUpOnASilentCLI(t *testing.T) {
	fakeclaude.OnPath(t, fakeclaude.Hang, "")
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	info := DiscoverProvider(ctx, &config.Config{ClaudeModel: "opus"}, "claude")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("discovery took %v past a 300ms deadline", elapsed)
	}
	if want := []string{"default", "opus"}; !info.Available || !slices.Equal(info.Models, want) {
		t.Fatalf("claude = %+v, want available with %v", info, want)
	}
}

func TestClaudeDiscoveryStopsWithItsCaller(t *testing.T) {
	t.Run("cancelled while the CLI runs", func(t *testing.T) {
		cli := fakeclaude.OnPath(t, fakeclaude.Hang, "")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan ProviderInfo, 1)
		go func() { done <- DiscoverProvider(ctx, &config.Config{ClaudeModel: "opus"}, "claude") }()

		deadline := time.Now().Add(5 * time.Second)
		for len(cli.Runs(t)) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the CLI never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		select {
		case info := <-done:
			if want := []string{"default", "opus"}; !slices.Equal(info.Models, want) {
				t.Fatalf("models = %q, want %q", info.Models, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("discovery outlived its caller's context")
		}
	})

	t.Run("cancelled before it starts", func(t *testing.T) {
		cli := fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		info := DiscoverProvider(ctx, &config.Config{}, "claude")
		if !info.Available || !slices.Equal(info.Models, []string{"default"}) {
			t.Fatalf("claude = %+v, want available with default only", info)
		}
		if runs := cli.Runs(t); len(runs) != 0 {
			t.Fatalf("CLI ran %d times after the caller gave up", len(runs))
		}
	})
}

func TestClaudeModelsLeaveOtherProvidersAlone(t *testing.T) {
	fakeclaude.OnPath(t, fakeclaude.Answer, fakeclaude.Models)
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"models":[{"name":"local-model:latest"}]}`)
	}))
	defer server.Close()

	got := map[string]ProviderInfo{}
	for _, p := range DiscoverProvidersContext(t.Context(), &config.Config{OllamaHost: server.URL}) {
		got[p.Name] = p
	}
	for _, spec := range brain.Providers() {
		if _, ok := got[spec.Name]; !ok {
			t.Errorf("provider %q missing; got %v", spec.Name, got)
		}
	}
	if ollama := got["ollama"]; !ollama.Available || !slices.Equal(ollama.Models, []string{"local-model"}) {
		t.Errorf("ollama = %+v", ollama)
	}
	if claude := got["claude"]; !slices.Equal(claude.Models, cliModels) {
		t.Errorf("claude = %+v", claude)
	}
}
