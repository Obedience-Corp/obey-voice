//go:build darwin || linux

package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Only the external CLI is replaced; tests exercise the released SDK parser,
// subprocess lifecycle, stdin transport, and Voice adapter with isolated files.
func init() {
	root := os.Getenv("OBEY_VOICE_SDK_FIXTURE")
	if root == "" {
		return
	}
	args := os.Args[1:]
	var socket string
	if len(args) > 1 && args[0] == "--socket" {
		socket = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		os.Exit(90)
	}
	if args[0] == "doctor" {
		fmt.Println(`{"schema":"obey.doctor","version":3,"status":"ok","capabilities":{"session_run":{"protocol":"obey.session.run","versions":[1]}}}`)
		os.Exit(0)
	}
	if args[0] != "run" {
		os.Exit(91)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(92)
	}
	cwd, _ := os.Getwd()
	receipt, _ := json.Marshal(map[string]any{"args": args, "socket": socket, "prompt": string(input), "cwd": cwd})
	if os.WriteFile(filepath.Join(root, "request.json"), receipt, 0600) != nil {
		os.Exit(93)
	}
	emit := func(kind string, fields map[string]any) {
		fields["protocol"] = "obey.session.run"
		fields["version"] = 1
		fields["type"] = kind
		_ = json.NewEncoder(os.Stdout).Encode(fields)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	emit("session_created", map[string]any{"session_id": "fixture-session", "campaign_id": "fixture-camp"})
	if os.Getenv("OBEY_VOICE_SDK_FIXTURE_CANCEL") == "1" {
		<-sig
		_ = os.WriteFile(filepath.Join(root, "canceled"), []byte("owned run stopped"), 0600)
		emit("error", map[string]any{"session_id": "fixture-session", "code": "canceled", "message": "cancelled"})
		emit("terminal", map[string]any{"session_id": "fixture-session", "status": "canceled"})
		os.Exit(7)
	}
	emit("assistant_content", map[string]any{"session_id": "fixture-session", "content": "Ada: Transport works.", "final": true})
	emit("usage", map[string]any{"session_id": "fixture-session", "input_tokens": 3, "output_tokens": 2, "cached_tokens": 0, "latency_ms": 1})
	emit("terminal", map[string]any{"session_id": "fixture-session", "status": "completed"})
	os.Exit(0)
}
func installObeyFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Symlink keeps the SDK's argv and stdin intact, with no shell interpolation.
	if err := os.Symlink(binary, filepath.Join(root, "obey")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_VOICE_SDK_FIXTURE", root)
	cfg := &config.Config{BrainProvider: "obey", ObeyProvider: "fixture-provider", ObeyModel: "fixture-model", ObeyCamp: "fixture-camp", ObeySocket: filepath.Join(root, "daemon.sock"), ObeyToolPolicy: "daemon", WorkDir: root, WorkDirSource: "fixture", AgentName: "Ada", PromptsDir: root}
	return cfg, root
}
func TestObeySDKTransportAndCleanup(t *testing.T) {
	cfg, root := installObeyFixture(t)
	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, err := provider.ThinkFull(t.Context(), "hello from voice", StreamOptions{})
	if err != nil || text != "Transport works." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	var request struct {
		Args                []string
		Socket, Prompt, Cwd string
	}
	data, err := os.ReadFile(filepath.Join(root, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if request.Cwd != resolvedRoot || request.Socket != cfg.ObeySocket || !strings.Contains(request.Prompt, "hello from voice") {
		t.Fatalf("request=%+v", request)
	}
	for _, want := range []string{"--provider fixture-provider", "--model fixture-model", "--campaign fixture-camp", "--mode discussion", "--input -"} {
		if !strings.Contains(strings.Join(request.Args, " "), want) {
			t.Errorf("missing %q in %v", want, request.Args)
		}
	}
	t.Setenv("OBEY_VOICE_SDK_FIXTURE_CANCEL", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := (obeySDKRunner{cfg: cfg}).run(ctx, "cancel this run", &obey.RunOptions{SessionOptions: obey.SessionOptions{Provider: cfg.ObeyProvider, Model: cfg.ObeyModel}}, func(ev obey.Event) error {
		if ev.Kind == obey.EventSessionCreated {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if result == nil || result.Cleanup != obey.CleanupStopped {
		t.Fatalf("cleanup=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "canceled")); err != nil {
		t.Fatalf("CLI cleanup was not observed: %v", err)
	}
}
