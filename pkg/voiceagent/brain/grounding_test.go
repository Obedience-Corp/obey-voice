package brain

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type tagsModel struct {
	Name         string   `json:"name"`
	ModifiedAt   string   `json:"modified_at"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// tagsServer answers /api/tags with models, reversing the order on every
// other call, and counts the calls.
func tagsServer(t *testing.T, models []tagsModel) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		listed := slices.Clone(models)
		if calls.Add(1)%2 == 0 {
			slices.Reverse(listed)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": listed})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func groundingConfig(t *testing.T, host string) *config.Config {
	cfg := ollamaConstructorConfig(t, host)
	cfg.OllamaEmbeddingModel = ""
	cfg.SkillsEnabled = false
	cfg.EnvironmentContextEnabled = true
	cfg.RuntimeEnvUser, cfg.RuntimeEnvHostname, cfg.RuntimeEnvOS = "lance", "host", "darwin/arm64"
	return cfg
}

var groundingModels = []tagsModel{
	{Name: "chat-model", ModifiedAt: "2026-10-30T04:47:25Z", Capabilities: []string{"completion", "tools"}},
	{Name: "older-chat:7b", ModifiedAt: "2025-05-21T22:03:06Z", Capabilities: []string{"completion"}},
	{Name: "nomic-embed-text:latest", ModifiedAt: "2026-11-01T00:00:00Z", Capabilities: []string{"embedding"}},
	{Name: "newest-chat:30b", ModifiedAt: "2026-11-13T07:51:41Z", Capabilities: []string{"completion", "vision"}},
	{Name: "a-twin:1b", ModifiedAt: "2026-10-30T04:47:25Z", Capabilities: []string{"completion"}},
}

// The environment block sits near the start of the system prompt, so any byte
// that moves between turns makes the server re-read the whole conversation.
func TestOllamaEnvironmentBlockIsByteStableAcrossTurnsAndListOrder(t *testing.T) {
	srv, calls := tagsServer(t, groundingModels)

	first, err := newOllama(t.Context(), groundingConfig(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	want := first.fullSystemPrompt
	for turn := range 3 {
		first.refreshSystemPrompt(nil)
		if got := first.fullSystemPrompt; got != want {
			t.Fatalf("turn %d changed the system prompt:\nwas %q\nnow %q", turn+1, want, got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("installed models listed %d times, want once per brain", n)
	}

	// The second brain receives the list in reverse order.
	second, err := newOllama(t.Context(), groundingConfig(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	gotBlock, wantBlock := environmentLines(second.fullSystemPrompt), environmentLines(want)
	if gotBlock != wantBlock {
		t.Fatalf("a reordered model list changed the block:\nwas %q\nnow %q", wantBlock, gotBlock)
	}
	if !strings.Contains(want, "- Installed local models: newest-chat:30b, a-twin:1b, chat-model, older-chat:7b\n") {
		t.Fatalf("installed line not newest first then by name, or lists a non-chat model:\n%s", want)
	}
}

// environmentLines returns the block's lines; the working directory, and the
// lines derived from it, differ between brains built from separate temp configs.
func environmentLines(prompt string) string {
	var keep []string
	for line := range strings.SplitSeq(prompt, "\n") {
		if strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "- Working directory:") && !strings.HasPrefix(line, "- Campaign:") && !strings.HasPrefix(line, "- An @path") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

func TestEnvironmentDateChangesOnlyWithTheDay(t *testing.T) {
	cfg := &config.Config{EnvironmentContextEnabled: true, OllamaModel: "chat-model", RuntimeEnvUser: "lance", RuntimeEnvHostname: "host", RuntimeEnvOS: "darwin/arm64"}
	render := func(now time.Time) string {
		return AssembleSystemPrompt(SystemPromptInput{Provider: providerOllama, Persona: "You are Obey.", WorkDir: "/work", Cfg: cfg, Now: now})
	}
	day := time.Date(2026, 11, 16, 0, 0, 1, 0, time.Local)
	morning := render(day)
	for _, later := range []time.Time{day.Add(9 * time.Hour), day.Add(23*time.Hour + 59*time.Minute + 58*time.Second)} {
		if got := render(later); got != morning {
			t.Fatalf("prompt changed within the day at %s:\nwas %q\nnow %q", later.Format(time.TimeOnly), morning, got)
		}
	}
	next := render(day.AddDate(0, 0, 1))
	if next == morning {
		t.Fatal("prompt did not change when the day did")
	}
	if !strings.Contains(morning, "- Today's date: Monday, 16 November 2026\n") || !strings.Contains(next, "- Today's date: Tuesday, 17 November 2026\n") {
		t.Fatalf("date lines wrong:\n%s\n---\n%s", morning, next)
	}
	if strings.Replace(next, "Tuesday, 17 November", "Monday, 16 November", 1) != morning {
		t.Fatal("a new day changed more than the date line")
	}
}

func TestEnvironmentBlockForOllamaAndOtherProviders(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var installed []InstalledModel
	for _, m := range groundingModels {
		installed = append(installed, InstalledModel{Name: m.Name, Chat: !slices.Contains(m.Capabilities, "embedding")})
	}
	base := config.Config{EnvironmentContextEnabled: true, OllamaModel: "chat-model", ClaudeModel: "", GrokModel: "grok-4", RuntimeEnvUser: "lance", RuntimeEnvHostname: "host", RuntimeEnvOS: "darwin/arm64"}
	render := func(provider string, cfg config.Config) string {
		return AssembleSystemPrompt(SystemPromptInput{Provider: provider, Persona: "You are Obey.", WorkDir: "/work", Cfg: &cfg, Installed: installed, Now: now})
	}
	t.Run("ollama", func(t *testing.T) {
		got := render(providerOllama, base)
		for _, want := range []string{
			"- Running on: chat-model via ollama\n",
			"- Installed local models: a-twin:1b, chat-model, newest-chat:30b, older-chat:7b\n",
			"- Today's date: Monday, 16 November 2026\n",
			"- Your built-in knowledge stops at your training cutoff, so trust today's date and the installed models listed here over what you remember, and for anything newer than your training say you may be out of date instead of presenting old information as current.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})
	t.Run("claude with its default model", func(t *testing.T) {
		got := render(providerClaude, base)
		if !strings.Contains(got, "- Running on: claude with its default model\n") {
			t.Errorf("brain line missing:\n%s", got)
		}
		if !strings.Contains(got, "so trust today's date as given here over what you remember") {
			t.Errorf("guidance should name only the date when no models are listed:\n%s", got)
		}
	})
	t.Run("grok", func(t *testing.T) {
		if got := render(providerGrok, base); !strings.Contains(got, "- Running on: grok-4 via grok\n") {
			t.Errorf("brain line missing:\n%s", got)
		}
	})
	t.Run("worker gets no installed list", func(t *testing.T) {
		cfg := base
		cfg.IsWorker = true
		got := render(providerOllama, cfg)
		if strings.Contains(got, "Installed local models") || strings.Contains(got, "older-chat") {
			t.Errorf("worker prompt lists installed models:\n%s", got)
		}
		if !strings.Contains(got, "- Today's date: ") || !strings.Contains(got, "- Running on: chat-model via ollama") {
			t.Errorf("worker prompt lost the date or brain line:\n%s", got)
		}
	})
	t.Run("environment_context_enabled off removes the whole block", func(t *testing.T) {
		cfg := base
		cfg.EnvironmentContextEnabled = false
		got := render(providerOllama, cfg)
		for _, gone := range []string{"Environment:", "Running on", "Installed local models", "Today's date", "training cutoff", "newest-chat"} {
			if strings.Contains(got, gone) {
				t.Errorf("%q survived environment_context_enabled=false:\n%s", gone, got)
			}
		}
	})
	t.Run("supplied env never falls back to the host's models", func(t *testing.T) {
		cfg := base
		cfg.RuntimeEnvSupplied = true
		got := render(providerOllama, cfg)
		if strings.Contains(got, "Installed local models") || strings.Contains(got, "newest-chat") {
			t.Errorf("supplied env leaked the host's models:\n%s", got)
		}
		cfg.RuntimeEnvModels = []string{"svc-b", "svc-a"}
		cfg.RuntimeEnvOmitDate, cfg.RuntimeEnvOmitBrain = true, true
		got = render(providerOllama, cfg)
		if !strings.Contains(got, "- Installed local models: svc-a, svc-b\n") {
			t.Errorf("supplied models not listed:\n%s", got)
		}
		if strings.Contains(got, "Today's date") || strings.Contains(got, "Running on") {
			t.Errorf("omitted lines rendered:\n%s", got)
		}
	})
	t.Run("supplied location dates the block", func(t *testing.T) {
		cfg := base
		cfg.RuntimeEnvSupplied = true
		cfg.RuntimeEnvLocation = time.FixedZone("UTC+13", 13*3600)
		if got := render(providerOllama, cfg); !strings.Contains(got, "- Today's date: Tuesday, 17 November 2026\n") {
			t.Errorf("date not reckoned in the supplied zone:\n%s", got)
		}
	})
}

func TestInstalledModelsLineIsBounded(t *testing.T) {
	var models []InstalledModel
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 25 {
		models = append(models, InstalledModel{Name: fmt.Sprintf("model-%02d", i), Modified: start.AddDate(0, 0, i), Chat: true})
	}
	long := "hf.co/lordx64/Qwen3.6-35B-A3B-Kimi-K2.6-Reasoning-Distilled-GGUF:IQ4_XS"
	models = append(models,
		InstalledModel{Name: long, Modified: start.AddDate(1, 0, 0), Chat: true},
		InstalledModel{Name: "embed-only", Modified: start.AddDate(2, 0, 0), Chat: false},
	)
	got := installedModelsLine(models)
	names := strings.Split(strings.TrimSuffix(got, ", and 6 more"), ", ")
	if !strings.HasSuffix(got, ", and 6 more") || len(names) != maxGroundingModels {
		t.Fatalf("want %d names then \"and 6 more\", got %q", maxGroundingModels, got)
	}
	if names[0] != "hf.co/lordx64/Qwen3.6-35B-A3B-Kimi-K2.6…" {
		t.Errorf("long name not capped at %d runes: %q", maxGroundingModelName, names[0])
	}
	if names[1] != "model-24" || strings.Contains(got, "embed-only") {
		t.Errorf("not newest chat models first: %q", got)
	}
	if installedModelsLine([]InstalledModel{{Name: "embed-only"}}) != "" {
		t.Error("a list with no chat model should render no line")
	}
}

func TestInstalledModelsKeepsModelsWithUnreportedCapabilities(t *testing.T) {
	srv, _ := tagsServer(t, []tagsModel{{Name: "chat-model", ModifiedAt: "2026-10-30T04:47:25Z"}, {Name: "old-server-model:latest", ModifiedAt: "2026-10-01T00:00:00Z"}})
	brain, err := newOllama(t.Context(), groundingConfig(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brain.fullSystemPrompt, "- Installed local models: chat-model, old-server-model:latest\n") {
		t.Fatalf("models without reported capabilities were dropped:\n%s", brain.fullSystemPrompt)
	}
}
