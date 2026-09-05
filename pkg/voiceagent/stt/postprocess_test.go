package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

// ollamaStub answers /api/tags with one model and /api/chat with a fixed
// reply, recording every chat request body.
type ollamaStub struct {
	srv    *httptest.Server
	reply  string
	mu     sync.Mutex
	bodies []map[string]any
	warm   bool
}

func newOllamaStub(t *testing.T, model, reply string) *ollamaStub {
	t.Helper()
	s := &ollamaStub{reply: reply}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": model}}})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		if msgs, _ := body["messages"].([]any); len(msgs) == 0 {
			s.warm = true
		} else {
			s.bodies = append(s.bodies, body)
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": model, "done": true,
			"message": map[string]any{"role": "assistant", "content": s.reply},
		})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *ollamaStub) warmed() bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		w := s.warm
		s.mu.Unlock()
		if w {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (s *ollamaStub) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

func postprocessConfig(t *testing.T, host string) *config.Config {
	t.Helper()
	return &config.Config{
		OllamaHost:                 host,
		OllamaModel:                "brain-model",
		OllamaKeepAlive:            "10m",
		OllamaThink:                "on",
		STTCustomWords:             []string{"Kokoro!"},
		STTPostprocessEnabled:      true,
		STTPostprocessProvider:     "ollama",
		STTPostprocessModel:        "small",
		STTPostprocessMode:         "patch",
		STTPostprocessTimeoutMS:    500,
		STTPostprocessKeepAlive:    "-1",
		PromptsDir:                 t.TempDir(),
		AgentName:                  "Obey",
		PersonaDisplayNames:        []string{"Samantha"},
		STTCustomWordsFromPersonas: true,
	}
}

func TestPostprocessFromConfigDisabled(t *testing.T) {
	m, rep := PostprocessFromConfig(&config.Config{}, nil, nil)
	if m != nil || rep.Enabled {
		t.Fatalf("disabled stage built: %+v", rep)
	}
}

func TestPostprocessFromConfigErrors(t *testing.T) {
	stub := newOllamaStub(t, "small", `{"substitutions":[]}`)
	tests := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{"unknown mode", func(c *config.Config) { c.STTPostprocessMode = "shout" }, "unknown post-processing mode"},
		{"model not pulled", func(c *config.Config) { c.STTPostprocessModel = "missing" }, "ollama pull missing"},
		{"server down", func(c *config.Config) { c.OllamaHost = "http://127.0.0.1:9" }, "cannot connect to ollama"},
		{"no model anywhere", func(c *config.Config) { c.STTPostprocessModel = ""; c.OllamaModel = "" }, "ollama_model not configured"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := postprocessConfig(t, stub.srv.URL)
			tc.edit(cfg)
			m, rep := PostprocessFromConfig(cfg, nil, nil)
			if m != nil || !rep.Enabled || !strings.Contains(rep.Error, tc.want) {
				t.Fatalf("stage %v report %+v", m != nil, rep)
			}
		})
	}
}

func TestPostprocessDerivedConfigNeverMutatesLoaded(t *testing.T) {
	stub := newOllamaStub(t, "small", `{"substitutions":[]}`)
	cfg := postprocessConfig(t, stub.srv.URL)
	before := *cfg
	m, rep := PostprocessFromConfig(cfg, nil, nil)
	if m == nil || rep.Error != "" {
		t.Fatalf("stage not built: %+v", rep)
	}
	if cfg.BrainProvider != before.BrainProvider || cfg.OllamaModel != before.OllamaModel || cfg.OllamaThink != before.OllamaThink || cfg.OllamaKeepAlive != before.OllamaKeepAlive {
		t.Fatalf("loaded config mutated: %+v", cfg)
	}
	if rep.Provider != "ollama" || rep.Model != "small" || rep.Mode != correct.ModePatch || rep.Timeout.Milliseconds() != 500 || rep.Prompt != "embedded" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPostprocessFallsBackToProviderModel(t *testing.T) {
	stub := newOllamaStub(t, "brain-model", `{"substitutions":[]}`)
	cfg := postprocessConfig(t, stub.srv.URL)
	cfg.STTPostprocessModel = ""
	_, rep := PostprocessFromConfig(cfg, nil, nil)
	if rep.Error != "" || rep.Model != "brain-model" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestStagesFromConfigRunsTheModelBetweenWordsAndNumbers(t *testing.T) {
	stub := newOllamaStub(t, "small", `{"substitutions":[{"from":"coco row","to":"Kokoro"}]}`)
	cfg := postprocessConfig(t, stub.srv.URL)
	cfg.STTNumbers = true
	cfg.STTFillerRemoval = true
	chain, report := StagesFromConfig(cfg)
	if strings.Join(report.Stages, ",") != "fillers,custom_words,model,numbers" {
		t.Fatalf("stages = %v (%+v)", report.Stages, report.Postprocess)
	}
	got, err := chain.Correct(context.Background(), "um use coco row for thirty five voices")
	if err != nil || got != "Use Kokoro for 35 voices" {
		t.Fatalf("got %q, %v", got, err)
	}
	reqs := stub.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	body := reqs[0]
	if body["model"] != "small" || body["think"] != false || body["keep_alive"] == nil {
		t.Fatalf("chat request = %v", body)
	}
	msgs := body["messages"].([]any)
	system := msgs[0].(map[string]any)["content"].(string)
	user := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Known names: Kokoro, Obey, Samantha") || strings.Contains(system, "{known_names}") {
		t.Fatalf("system prompt = %q", system)
	}
	if !strings.Contains(user, "Use coco row for thirty five voices") || !strings.Contains(user, `"coco row" may be "Kokoro"`) {
		t.Fatalf("user message = %q", user)
	}
	if s := report.Model.Stats(); s.Calls != 1 || s.Applied != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if !stub.warmed() {
		t.Fatal("model was not warmed up")
	}
	// No candidates: no call.
	if got, _ := chain.Correct(context.Background(), "nothing near a name"); got != "nothing near a name" || len(stub.requests()) != 1 {
		t.Fatalf("model was called without candidates: %q, %d requests", got, len(stub.requests()))
	}
}

func TestPostprocessPromptReloadsFromUserFile(t *testing.T) {
	stub := newOllamaStub(t, "small", `{"substitutions":[]}`)
	cfg := postprocessConfig(t, stub.srv.URL)
	dir := filepath.Join(cfg.PromptsDir, "postprocess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(instruction string) {
		doc := "schema: obey-voice.prompt.v1\nprompt:\n  name: patch\n  kind: postprocess\n  system_prompt: |-\n    " + instruction + "\n    Known names: {known_names}\nmetadata:\n  id: t\n  version: 1\n  description: test\n"
		if err := os.WriteFile(filepath.Join(dir, "patch.yaml"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("FIRST INSTRUCTION")
	m, rep := PostprocessFromConfig(cfg, nil, []correct.Term{{Text: "Kokoro"}})
	if m == nil || !strings.HasSuffix(rep.Prompt, filepath.Join("postprocess", "patch.yaml")) {
		t.Fatalf("report = %+v", rep)
	}
	if text := m.Prompt(); !strings.Contains(text, "FIRST INSTRUCTION") || !strings.Contains(text, "Known names: Kokoro") {
		t.Fatalf("prompt = %q", text)
	}
	write("SECOND INSTRUCTION")
	if text := m.Prompt(); !strings.Contains(text, "SECOND INSTRUCTION") {
		t.Fatalf("prompt did not reload: %q", text)
	}
	if err := os.WriteFile(filepath.Join(dir, "patch.yaml"), []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if text := m.Prompt(); !strings.Contains(text, "SECOND INSTRUCTION") {
		t.Fatalf("broken file should keep the last good prompt: %q", text)
	}
}

func TestPostprocessProviderEnumMatchesRegistry(t *testing.T) {
	spec, ok := config.SpecFor("stt_postprocess_provider")
	if !ok {
		t.Fatal("no spec for stt_postprocess_provider")
	}
	brainSpec, _ := config.SpecFor("brain_provider")
	if strings.Join(spec.Enum, ",") != strings.Join(brainSpec.Enum, ",") {
		t.Fatalf("enum %v differs from brain_provider %v", spec.Enum, brainSpec.Enum)
	}
}
