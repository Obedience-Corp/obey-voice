package brain

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper is a real subprocess speaking the wire protocol. No Codex login,
// model, audio device, or network is used by the normal test suite.
func TestCodexAppServerHelper(t *testing.T) {
	if os.Getenv("OBEY_CODEX_TEST_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := encoder.Encode(v); err != nil {
			os.Exit(2)
		}
	}
	notice := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
	logFile, err := os.OpenFile(os.Getenv("OBEY_CODEX_TEST_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	mode := os.Getenv("OBEY_CODEX_TEST_MODE")
	tools := false
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		_, _ = fmt.Fprintln(logFile, scanner.Text())
		_ = logFile.Sync()
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			os.Exit(2)
		}
		reply := func(r any) { send(map[string]any{"id": m.ID, "result": r}) }
		switch m.Method {
		case "initialize":
			if mode == "startup-hang" {
				continue
			}
			if mode == "malformed" {
				fmt.Println("not JSON")
				continue
			}
			if mode == "eof" {
				os.Exit(0)
			}
			reply(map[string]any{"userAgent": "fixture"})
		case "config/read":
			reply(map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"company.tools": map[string]any{"enabled": true}}, "plugins": map[string]any{"fixture@local": map[string]any{"enabled": true}}}})
		case "thread/start", "thread/resume":
			if mode == "rpc-error" {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": "sign in with codex login"}})
				continue
			}
			sandbox := "readOnly"
			if m.Params["sandbox"] == "workspace-write" {
				sandbox = "workspaceWrite"
				tools = true
			}
			reply(map[string]any{"thread": map[string]string{"id": "thread-1"}, "sandbox": map[string]string{"type": sandbox}, "approvalPolicy": "never", "cwd": m.Params["cwd"]})
		case "turn/start":
			notice("turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "inProgress"}})
			reply(map[string]any{"turn": map[string]string{"id": "turn-1", "status": "inProgress"}})
			if mode == "approval" {
				send(map[string]any{"id": "approval-1", "method": "item/commandExecution/requestApproval", "params": map[string]string{"threadId": "thread-1", "turnId": "turn-1"}})
				continue
			}
			if mode == "unknown-request" {
				send(map[string]any{"id": 55, "method": "future/interaction", "params": map[string]any{}})
				continue
			}
			notice("item/reasoning/textDelta", map[string]string{"threadId": "thread-1", "turnId": "turn-1", "delta": "PRIVATE REASONING"})
			notice("item/commandExecution/outputDelta", map[string]string{"threadId": "thread-1", "turnId": "turn-1", "delta": "PRIVATE OUTPUT"})
			notice("item/agentMessage/delta", map[string]string{"threadId": "another-thread", "turnId": "turn-1", "delta": "WRONG THREAD"})
			notice("item/agentMessage/delta", map[string]string{"threadId": "thread-1", "turnId": "another-turn", "delta": "WRONG TURN"})
			if tools || mode == "unexpected-tool" {
				for _, method := range []string{"item/started", "item/completed"} {
					notice(method, map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]string{"id": "tool-1", "type": "commandExecution", "status": "completed"}})
				}
			}
			notice("item/agentMessage/delta", map[string]string{"threadId": "thread-1", "turnId": "turn-1", "delta": "Hello there."})
			if mode == "cancel" || mode == "backpressure" {
				continue
			}
			time.Sleep(100 * time.Millisecond)
			notice("thread/tokenUsage/updated", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 42, "outputTokens": 7}}})
			status := "completed"
			if mode == "failed" {
				status = "failed"
			}
			if mode == "interrupted" {
				status = "interrupted"
			}
			notice("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": status, "error": map[string]string{"message": "fixture failure"}}})
		case "turn/interrupt":
			reply(map[string]any{})
		case "model/list":
			reply(map[string]any{"data": []any{map[string]string{"model": "fixture-model"}}, "nextCursor": nil})
		}
	}
	_ = logFile.Close()
	os.Exit(0)
}
func codexProcessFixture(t *testing.T, mode string) (codexClient, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "codex")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err = os.WriteFile(shim, []byte("#!/bin/sh\nexec "+quote(binary)+" -test.run='^TestCodexAppServerHelper$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "requests.jsonl")
	t.Setenv("OBEY_CODEX_TEST_HELPER", "1")
	t.Setenv("OBEY_CODEX_TEST_MODE", mode)
	t.Setenv("OBEY_CODEX_TEST_LOG", log)
	return codexClient{binary: shim}, log
}
func TestCodexProtocolStreamsOnlyAssistantAndEnforcesPolicy(t *testing.T) {
	client, log := codexProcessFixture(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var text strings.Builder
	var first time.Time
	started := time.Now()
	starts, ends := 0, 0
	result, err := client.run(ctx, codexRequest{Cwd: t.TempDir(), Prompt: "hello", System: "persona", Model: "fixture-model", Effort: "low"}, func(s string) error {
		if first.IsZero() {
			first = time.Now()
		}
		text.WriteString(s)
		return nil
	}, StreamOptions{OnToolStart: func(ToolCall) { starts++ }, OnToolEnd: func(ToolResult) { ends++ }})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello there." {
		t.Fatalf("non-assistant text leaked: %q", text.String())
	}
	if first.IsZero() || time.Since(first) < 90*time.Millisecond {
		t.Fatalf("not streaming before completion: first=%v total=%v", first.Sub(started), time.Since(started))
	}
	if starts != 0 || ends != 0 || result.InputTokens == nil || *result.InputTokens != 42 || *result.OutputTokens != 7 {
		t.Fatalf("missing trace/usage: %d %d %+v", starts, ends, result)
	}
	data, _ := os.ReadFile(log)
	var thread map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		var m struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal([]byte(line), &m)
		if m.Method == "thread/start" {
			thread = m.Params
		}
	}
	if thread["sandbox"] != "read-only" || thread["approvalPolicy"] != "never" {
		t.Fatalf("unsafe policy: %+v", thread)
	}
	policy := thread["config"].(map[string]any)
	for _, key := range []string{"features.shell_tool", "features.unified_exec", "features.apps"} {
		if policy[key] != false {
			t.Fatalf("integration not disabled: %s=%v", key, policy[key])
		}
	}
	for key, name := range map[string]string{"mcp_servers": "company.tools", "plugins": "fixture@local"} {
		if policy[key].(map[string]any)[name].(map[string]any)["enabled"] != false {
			t.Fatalf("%s not disabled", key)
		}
	}
}
func TestCodexProtocolFailures(t *testing.T) {
	for _, mode := range []string{"malformed", "eof", "rpc-error", "failed", "interrupted", "approval", "unknown-request", "unexpected-tool"} {
		t.Run(mode, func(t *testing.T) {
			client, log := codexProcessFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := client.run(ctx, codexRequest{Cwd: t.TempDir(), Prompt: "hello"}, func(string) error { return nil }, StreamOptions{})
			if err == nil {
				t.Fatal("accepted failed operation")
			}
			if mode == "approval" || mode == "unknown-request" {
				data, _ := os.ReadFile(log)
				if mode == "approval" && !strings.Contains(string(data), `"decision":"cancel"`) {
					t.Fatal("approval was not rejected")
				}
				if mode == "unknown-request" && !strings.Contains(string(data), `"code":-32601`) {
					t.Fatal("unknown request was not answered")
				}
			}
		})
	}
}
func TestCodexCancellationInterruptsAndReaps(t *testing.T) {
	for _, mode := range []string{"startup-hang", "cancel", "backpressure"} {
		t.Run(mode, func(t *testing.T) {
			client, log := codexProcessFixture(t, mode)
			deadline := 3 * time.Second
			if mode == "startup-hang" {
				deadline = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			started := time.Now()
			_, err := client.run(ctx, codexRequest{Cwd: t.TempDir(), Prompt: "hello"}, func(string) error {
				if mode == "backpressure" {
					time.AfterFunc(50*time.Millisecond, cancel)
					<-ctx.Done()
					return ctx.Err()
				}
				cancel()
				return ctx.Err()
			}, StreamOptions{})
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wrong cancellation error: %v", err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancellation not bounded")
			}
			if mode != "startup-hang" {
				data, _ := os.ReadFile(log)
				if !strings.Contains(string(data), `"method":"turn/interrupt"`) {
					t.Fatal("missing explicit interrupt")
				}
			}
		})
	}
}

func TestCodexToolsTraceAndResumeUsage(t *testing.T) {
	client, log := codexProcessFixture(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	starts, ends := 0, 0
	result, err := client.run(ctx, codexRequest{Cwd: t.TempDir(), ThreadID: "thread-1", Tools: true, Prompt: "work"}, func(string) error { return nil }, StreamOptions{OnToolStart: func(ToolCall) { starts++ }, OnToolEnd: func(ToolResult) { ends++ }})
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || ends != 1 {
		t.Fatal("tool trace missing")
	}
	if result.InputTokens != nil || result.OutputTokens != nil {
		t.Fatal("resumed cumulative usage misreported as new usage")
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), `"method":"thread/resume"`) || !strings.Contains(string(data), `"sandbox":"workspace-write"`) {
		t.Fatal("resume/tool policy missing")
	}
}
