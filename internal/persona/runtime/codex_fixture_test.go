package personaruntime

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real CodexBrain/selector/pipeline, with only the external app-server replaced.
// No login, network, audio hardware, or user configuration is used.
func installCodexSelectorFixture(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(binary) + " -test.run='^TestCodexSelectorAppServer$'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "requests.jsonl")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_CODEX_SELECTOR_LOG", log)
	return log
}

func TestCodexSelectorAppServer(t *testing.T) {
	log := os.Getenv("OBEY_CODEX_SELECTOR_LOG")
	if log == "" {
		return
	}
	file, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if encoder.Encode(v) != nil {
			os.Exit(2)
		}
	}
	notice := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		if _, err := file.Write(append(scanner.Bytes(), '\n')); err != nil {
			os.Exit(2)
		}
		var m struct {
			ID     json.RawMessage
			Method string
			Params map[string]any
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			os.Exit(2)
		}
		reply := func(r any) { send(map[string]any{"id": m.ID, "result": r}) }
		switch m.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "fixture"})
		case "config/read":
			reply(map[string]any{"config": map[string]any{}})
		case "thread/start", "thread/resume":
			reply(map[string]any{"thread": map[string]string{"id": "fixture-thread"}, "sandbox": map[string]string{"type": "readOnly"}, "approvalPolicy": "never", "cwd": m.Params["cwd"]})
		case "turn/start":
			reply(map[string]any{"turn": map[string]string{"id": "fixture-turn", "status": "inProgress"}})
			notice("item/agentMessage/delta", map[string]string{"threadId": "fixture-thread", "turnId": "fixture-turn", "delta": "I remember cobalt."})
			notice("turn/completed", map[string]any{"threadId": "fixture-thread", "turn": map[string]string{"id": "fixture-turn", "status": "completed"}})
		}
	}
	_ = file.Close()
	os.Exit(0)
}
