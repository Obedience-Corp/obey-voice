package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func recoveryHistory() []brain.Turn {
	return []brain.Turn{
		{Role: "user", Content: "Remember the first fact: cobalt.", Speaker: "speaker-1"},
		{Role: "assistant", Content: "Samantha recorded cobalt.", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "response-a1"},
		{Role: "user", Content: "Santa, remember the second fact: amber.", Speaker: "speaker-1"},
		{Role: "assistant", Content: "Santa recorded amber.", PersonaID: "santa", PersonaDisplayName: "Santa", ResponseID: "response-b1"},
		{Role: "user", Content: "Read the local fixture.", Speaker: "speaker-1"},
		{Role: "assistant", Content: "I will inspect the fixture.", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "response-a2", ToolCalls: []brain.TurnToolCall{{ID: "call-fixture", Name: "read_file", Arguments: `{"path":"fixture.txt"}`}}},
		{Role: "tool", ToolName: "read_file", ToolCallID: "call-fixture", Content: "cobalt and amber"},
		{Role: "assistant", Content: "The fixture contains cobalt and amber.", PersonaID: "samantha", PersonaDisplayName: "Samantha", ResponseID: "response-a3"},
	}
}

func TestOllamaRecoveryCompactionPreservesCanonicalToolIdentityAndDiskReload(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		if req.URL.Path != "/api/chat" {
			http.NotFound(w, req)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode chat request: %v", err)
		}
		requests = append(requests, body)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"A compact summary preserving cobalt, amber, Samantha, Santa, and the fixture result."},"done":true}`+"\n")
	}))
	defer server.Close()

	root := t.TempDir()
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: "fixture", AgentName: "Samantha", PromptsDir: filepath.Join(root, "prompts"), WorkDir: root, MaxHistory: 20}
	o, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.CloseProvider(o) }()
	original := recoveryHistory()
	if err := o.ImportHistory(original); err != nil {
		t.Fatal(err)
	}

	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "fixture")
	if err := store.SaveSelection(sess, original, "samantha", "Samantha", "prompt-samantha"); err != nil {
		t.Fatal(err)
	}
	backupCalls := 0
	prompt, err := brain.CompactInstruction(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: o, CompactPrompt: prompt, OnCompactBackup: func(turns []brain.Turn) {
		backupCalls++
		if err := store.SaveBackup(sess, turns); err != nil {
			t.Errorf("SaveBackup: %v", err)
		}
	}}
	if err := p.CompactConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backupCalls != 1 {
		t.Fatalf("backup calls = %d, want one", backupCalls)
	}
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d, want one compaction request", len(requests))
	}

	if got := o.CanonicalHistory(); !reflect.DeepEqual(got, original) {
		t.Fatalf("canonical transcript changed during compaction:\ngot=%+v\nwant=%+v", got, original)
	}
	contextTurns := o.ContextHistory()
	if len(contextTurns) == 0 || !strings.Contains(contextTurns[0].Content, "compact summary") {
		t.Fatalf("context projection = %+v, want compact summary seed", contextTurns)
	}
	if len(contextTurns) < 4 || contextTurns[len(contextTurns)-2].ToolCallID != "call-fixture" || len(contextTurns[len(contextTurns)-3].ToolCalls) != 1 {
		t.Fatalf("context projection lost complete tool exchange: %+v", contextTurns)
	}
	callID, toolResult := "", false
	messages, _ := requests[0]["messages"].([]any)
	last, _ := messages[len(messages)-1].(map[string]any)
	instruction, _ := last["content"].(string)
	if !strings.Contains(instruction, "Preserve the historical persona name") || !strings.Contains(instruction, "including their disagreements") {
		t.Fatalf("actual compact request omitted historical attribution contract: %q", instruction)
	}

	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		content, _ := message["content"].(string)
		for _, turn := range original {
			if turn.ResponseID != "" && strings.Contains(content, turn.ResponseID) {
				t.Fatalf("internal response id exposed in compaction prompt: %s", content)
			}
		}
		if message["role"] == "assistant" {
			calls, _ := message["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				if id, _ := call["id"].(string); id != "" {
					callID = id
				}
			}
		}
		if message["role"] == "tool" && message["tool_call_id"] == "call-fixture" {
			toolResult = true
		}
	}
	if callID != "call-fixture" || !toolResult {
		t.Fatalf("native tool exchange in compact request lost pairing: call=%q result=%t body=%+v", callID, toolResult, requests[0])
	}

	backupPath := filepath.Join(root, "sessions", sess.ID+".pre-compact.json")
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	var backupSession session.Session
	if err := json.Unmarshal(backup, &backupSession); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupSession.Turns, original) {
		t.Fatalf("backup turns changed: got=%+v want=%+v", backupSession.Turns, original)
	}

	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Turns, original) {
		t.Fatalf("disk canonical turns changed: got=%+v want=%+v", reloaded.Turns, original)
	}
	second, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.CloseProvider(second) }()
	if err := second.ImportHistory(reloaded.Turns); err != nil {
		t.Fatal(err)
	}
	if got := second.CanonicalHistory(); !reflect.DeepEqual(got, original) || got[3].PersonaID != "santa" || got[5].PersonaID != "samantha" || got[6].ToolCallID != "call-fixture" {
		t.Fatalf("new provider import lost identity/tool pairing: %+v", got)
	}
}

func TestRecoveryScenarioSeedsFixtureAndOwnsCompactOperation(t *testing.T) {
	scn, err := loadScenario(filepath.Join("scenarios", "recovery-compaction.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if scn.HistoryFixture == "" || scn.Persona.Initial != "samantha" || len(scn.Turns) != 2 || scn.Turns[0].Op != "compact" {
		t.Fatalf("recovery scenario = %+v, want explicit fixture, Samantha, and compact operation", scn)
	}
}

func TestOllamaRecoveryImportPreservesLegacyAndHistoricalIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()
	cfg := &config.Config{OllamaHost: server.URL, OllamaModel: "fixture", PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
	o, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.CloseProvider(o) }()
	history := []brain.Turn{
		{Role: "samantha", Content: "old reply"}, // pre-identity session: no fabricated ID is allowed.
		{Role: "assistant", Content: "historical Santa reply", PersonaID: "santa", PersonaDisplayName: "Old Santa", ResponseID: "old-b"},
	}
	if err := o.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	got := o.CanonicalHistory()
	if got[0].Role != "assistant" || got[0].PersonaID != "" || got[0].ResponseID != "" {
		t.Fatalf("legacy turn migration fabricated identity: %+v", got[0])
	}
	if got[1].PersonaID != "santa" || got[1].PersonaDisplayName != "Old Santa" || got[1].ResponseID != "old-b" {
		t.Fatalf("historical identity changed while importing the fixture: %+v", got[1])
	}
}

func TestRecoveryHistoryFixtureRejectsEmptyInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &liveRunner{persona: &personaRuntime{}}
	if err := r.importCanonicalHistory(path); err == nil || !strings.Contains(err.Error(), "no turns") {
		t.Fatalf("empty fixture error = %v, want no turns", err)
	}
}

func configuredRecoveryRunner(t *testing.T, serverURL string, history []brain.Turn) *liveRunner {
	t.Helper()
	root := t.TempDir()
	cfgPath, personaDir, err := writeSelfTestFixture(root)
	if err != nil {
		t.Fatal(err)
	}
	base, err := loadFixtureConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	base.BrainProvider = "ollama"
	base.OllamaHost = serverURL
	base.OllamaModel = "fixture"
	base.VoiceWorkEnabled = true
	base.TTSProvider = "none"
	catalog := persona.Catalog{Dir: personaDir}
	initial, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		t.Fatal(err)
	}
	o, err := brain.NewOllama(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.ImportHistory(history); err != nil {
		t.Fatal(err)
	}
	worker := newStubWorker()
	bus := events.NewBus()
	agent, cleanup, err := voiceagent.New(t.Context(), voiceagent.Options{Config: initial.Config(), Events: bus, Brain: o, Worker: worker, TextOnly: true, Silent: true})
	if err != nil {
		_ = brain.CloseProvider(o)
		t.Fatal(err)
	}
	r := &liveRunner{agent: agent, cleanup: cleanup, liveProvider: o, live: &observing{inner: o}, workerProvider: worker, worker: &observing{inner: worker}}
	t.Cleanup(r.close)
	if err := configurePersonaWithFactory(r, base, catalog, initial, root, func(*config.Config) (brain.Provider, error) { return o, nil }); err != nil {
		r.close()
		t.Fatal(err)
	}
	return r
}

func TestConfiguredPersonaWrapperCompactionRestoresHooksAndSurfacesBackupFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/tags" {
			_, _ = io.WriteString(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		if req.URL.Path == "/api/chat" {
			_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"wrapper summary"},"done":true}`+"\n")
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()

	r := configuredRecoveryRunner(t, server.URL, recoveryHistory())
	p := r.agent.Pipeline
	previousPrompt := "previous compact prompt"
	p.CompactPrompt = previousPrompt
	sentinelCalls := 0
	p.OnCompactBackup = func([]brain.Turn) { sentinelCalls++ }
	if err := r.compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sentinelCalls != 1 || p.CompactPrompt != previousPrompt {
		t.Fatalf("first wrapper compact did not call/restore hooks: calls=%d prompt=%q", sentinelCalls, p.CompactPrompt)
	}
	// Calling the restored callback proves a second compact did not stack the
	// harness backup closure on top of the prior owner callback.
	p.OnCompactBackup(nil)
	if sentinelCalls != 2 {
		t.Fatalf("restored backup callback calls=%d, want 2", sentinelCalls)
	}
	if err := r.compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sentinelCalls != 3 {
		t.Fatalf("repeated wrapper compact stacked callbacks: calls=%d", sentinelCalls)
	}

	missingBackup := configuredRecoveryRunner(t, server.URL, recoveryHistory())
	missingBackup.persona.backupFn = func([]brain.Turn) error { return nil }
	if err := missingBackup.compact(t.Context()); err == nil || !strings.Contains(err.Error(), "backup is missing") {
		t.Fatalf("missing backup compact error=%v, want missing backup", err)
	}
	missingBackup.persona.mu.Lock()
	missingRows := append([]recoveryArtifact(nil), missingBackup.persona.recovery...)
	missingBackup.persona.mu.Unlock()
	if len(missingRows) != 1 || missingRows[0].Stage != "before" {
		t.Fatalf("missing backup recorded passing after artifact: %+v", missingRows)
	}

	r.persona.backupFn = func([]brain.Turn) error { return errors.New("forced backup failure") }
	if err := r.compact(t.Context()); err == nil || !strings.Contains(err.Error(), "forced backup failure") {
		t.Fatalf("failed backup compact error=%v, want forced backup failure", err)
	}
	r.persona.mu.Lock()
	failedRows := append([]recoveryArtifact(nil), r.persona.recovery...)
	r.persona.mu.Unlock()
	if len(failedRows) != 5 || failedRows[len(failedRows)-1].Stage != "before" {
		t.Fatalf("failed backup recorded passing after artifact: %+v", failedRows)
	}
	if p.CompactPrompt != previousPrompt || sentinelCalls != 4 {
		t.Fatalf("failed compact did not restore hooks: calls=%d prompt=%q", sentinelCalls, p.CompactPrompt)
	}
	if err := r.persona.validate(); err == nil || !strings.Contains(err.Error(), "forced backup failure") {
		t.Fatalf("failed backup validation=%v, want persistence error", err)
	}

	readError := configuredRecoveryRunner(t, server.URL, recoveryHistory())
	backupPath := filepath.Join(readError.persona.sessionDir, readError.persona.session.ID+".pre-compact.json")
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := readError.compact(t.Context()); err == nil || !strings.Contains(err.Error(), "backup read") {
		t.Fatalf("backup read error = %v, want non-not-exist read failure", err)
	}
}
