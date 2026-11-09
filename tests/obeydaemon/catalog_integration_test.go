//go:build integration

package obeydaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// This opt-in acceptance test runs only against an installed real binary in a
// disposable fixture. No private Obey source or live model credential is used.
func TestReleasedObeyCatalogAndVoiceTurn(t *testing.T) {
	if os.Getenv("VOICE_REAL_OBEY_CONTAINER") != "1" {
		t.Skip("requires explicit real-daemon container fixture")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("Docker fixture required before mutation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	workdir := filepath.Join(root, "camp")
	socket := filepath.Join(root, "daemon.sock")
	daemonConfig := filepath.Join(root, "daemon.json")
	command := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("command %s: %v: %s", args[0], err, out)
		}
		return out
	}
	version := string(command("obey", "version"))
	expected := os.Getenv("OBEY_EXPECT_VERSION")
	if expected == "" {
		expected = "0.5.0"
	}
	if !strings.Contains(version, "obey "+expected+"\n") {
		t.Fatalf("expected public Obey %s, got %s", expected, version)
	}
	command("camp", "init", workdir, "--name", "voice-catalog-fixture", "--type", "product", "--description", "Deterministic Voice catalog acceptance", "--mission", "Prove SDK route adoption", "--no-git", "--no-skills")
	var inference atomic.Int32
	var catalogCalls atomic.Int32
	const answer = "Fixture response from the Obey daemon."
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			catalogCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"object":"list","data":[{"id":"raw-fixture:1","object":"model","created":1,"owned_by":"fixture"}]}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		defer r.Body.Close()
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request) != nil || request.Model != "raw-fixture:1" || !request.Stream {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		inference.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []map[string]any{{"role": "assistant"}, {"content": answer}, {}} {
			frame := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "raw-fixture:1", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
			if len(delta) == 0 {
				frame["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": "stop"}}
			}
			raw, _ := json.Marshal(frame)
			fmt.Fprintf(w, "data: %s\n\n", raw)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	// The synthetic model has no built-in context metadata. Declare its fixture
	// budget so the real daemon can run its normal preflight before dispatch.
	data, _ := json.Marshal(map[string]any{"data_dir": filepath.Join(root, "data"), "campaigns": []string{workdir}, "grpc": map[string]any{"enabled": true, "socket": socket}, "network": map[string]any{"enabled": false}, "watcher": map[string]any{"enabled": false}, "auto_commits": map[string]any{"enabled": false}, "compaction": map[string]any{"window_overrides": map[string]int{"raw-fixture:1": 32768}}, "providers": map[string]any{"daemon-profile": map[string]any{"type": "openai", "enabled": true, "api_key": "fixture-not-a-secret", "base_url": provider.URL + "/v1", "default_model": "raw-fixture:1"}}})
	if err := os.WriteFile(daemonConfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBEY_CONFIG", daemonConfig)
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command("obey", "serve", "--config", daemonConfig, "--foreground", "--no-network", "--no-firewall-setup", "--no-watcher", "--socket", socket)
	daemon.Stdout = log
	daemon.Stderr = log
	if err := daemon.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = daemon.Wait(); close(done) }()
	defer func() {
		_ = daemon.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = daemon.Process.Kill()
			<-done
		}
		_ = log.Close()
	}()
	for {
		call, stop := context.WithTimeout(ctx, time.Second)
		err := exec.CommandContext(call, "obey", "--socket", socket, "session", "list", "--campaign", "voice-catalog-fixture", "--all", "--json").Run()
		stop()
		if err == nil {
			break
		}
		select {
		case <-done:
			output, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
			t.Fatalf("daemon exited: %v: %s", waitErr, output)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	cfg := &config.Config{BrainProvider: "obey", ObeyProvider: "daemon-profile", ObeyModel: "raw-fixture:1", ObeyCamp: "voice-catalog-fixture", ObeySocket: socket, ObeyToolPolicy: "strict", AgentName: "Fixture", WorkDir: workdir, WorkDirSource: "fixture", MaxHistory: 4}
	route := discovery.DiscoverObeyRoute(ctx, cfg, true)
	if route.Problem != "" || route.Providers == nil || route.Models == nil || len(route.Models.Models) != 1 || route.Models.Models[0].ID != "raw-fixture:1" || route.Readiness == nil || !route.Readiness.HasFailures() || inference.Load() != 0 {
		t.Fatalf("strict catalog/preflight failed: %+v inference=%d", route, inference.Load())
	}
	responder, err := brain.NewObey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ThinkFull(ctx, "strict must refuse", brain.StreamOptions{}); err == nil || inference.Load() != 0 || len(responder.History()) != 0 {
		t.Fatal("strict request did not refuse without inference/history mutation")
	}
	cfg.ObeyToolPolicy = "daemon"
	responder, err = brain.NewObey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	answerText, err := responder.ThinkFull(ctx, "Say the fixture response", brain.StreamOptions{})
	if err != nil || answerText != answer || inference.Load() != 1 || len(responder.History()) != 2 {
		t.Fatalf("reply=%q error=%v inference=%d history=%d", answerText, err, inference.Load(), len(responder.History()))
	}
	raw := command("obey", "--socket", socket, "session", "list", "--campaign", "voice-catalog-fixture", "--all", "--json")
	var rows []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		TurnCount uint64 `json:"turn_count"`
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].ID == "" || rows[0].Status != "SESSION_STATUS_STOPPED" || rows[0].TurnCount != 1 {
		t.Fatalf("persisted owned-session readback: %s", raw)
	}
	t.Logf("Public Obey "+expected+": provider/model picker IDs, strict refusal, one Voice answer, one persisted STOPPED turn; catalog requests=%d inference=1", catalogCalls.Load())
}
