package personaruntime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real CodexBrain/selector/pipeline, with only the external app-server replaced.
// No login, network, audio hardware, or user configuration is used.
func installCodexSelectorFixture(t *testing.T) string {
	t.Helper()
	binary := fixtureBinary(t)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nexec " + quote(binary) + " -test.run='^TestCodexSelectorAppServer$'\n"
	dir := fixtureScriptDir(t, "codex", map[string]string{"codex": script})
	log := filepath.Join(t.TempDir(), "requests.jsonl")
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
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	thread := "fixture-thread"
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
			// Distinct threads let a test tell a resumed thread from a new one.
			if os.Getenv("OBEY_CODEX_FIXTURE_THREADS") == "distinct" {
				if id, ok := m.Params["threadId"].(string); ok && m.Method == "thread/resume" {
					thread = id
				} else {
					thread = fmt.Sprintf("codex-thread-%d", os.Getpid())
				}
			}
			reply(map[string]any{"thread": map[string]string{"id": thread}, "sandbox": map[string]string{"type": "readOnly"}, "approvalPolicy": "never", "cwd": m.Params["cwd"]})
		case "turn/start":
			reply(map[string]any{"turn": map[string]string{"id": "fixture-turn", "status": "inProgress"}})
			if codexFixtureAsks(m.Params, codexFail) {
				notice("item/agentMessage/delta", map[string]string{"threadId": thread, "turnId": "fixture-turn", "delta": claudePartial})
				notice("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "fixture-turn", "status": "failed", "error": map[string]string{"message": "fixture failure"}}})
				continue
			}
			if codexFixtureStalls(m.Params) {
				notice("item/agentMessage/delta", map[string]string{"threadId": thread, "turnId": "fixture-turn", "delta": claudePartial})
				time.Sleep(time.Minute)
				os.Exit(0)
			}
			notice("item/agentMessage/delta", map[string]string{"threadId": thread, "turnId": "fixture-turn", "delta": "I remember cobalt."})
			notice("turn/completed", map[string]any{"threadId": thread, "turn": map[string]string{"id": "fixture-turn", "status": "completed"}})
		}
	}
	_ = file.Close()
	os.Exit(0)
}

// codexFail in a request makes the fixture send part of a reply and then
// report the turn failed.
const codexFail = "FAILNOW"

// codexFixtureStalls reports whether the request itself, not a conversation
// record quoting an earlier one, asks the fixture to stall.
func codexFixtureStalls(params map[string]any) bool { return codexFixtureAsks(params, claudeStall) }

// codexFixtureAsks reports whether the request itself, not a conversation
// record quoting an earlier one, carries marker.
func codexFixtureAsks(params map[string]any, marker string) bool {
	inputs, _ := params["input"].([]any)
	for _, in := range inputs {
		item, _ := in.(map[string]any)
		text, _ := item["text"].(string)
		for line := range strings.Lines(text) {
			if !strings.HasPrefix(line, "{") && strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

// codexTurn is one turn the Codex fixture was sent: its prompt, its thread,
// and whether that thread was resumed rather than started.
type codexTurn struct {
	Prompt, Thread string
	Resumed        bool
}

func readCodexTurns(t *testing.T, log string) []codexTurn {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []codexTurn
	resumed := false
	for line := range strings.Lines(string(data)) {
		var m struct {
			Method string
			Params struct {
				ThreadID string
				Input    []struct{ Text string }
			}
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		switch m.Method {
		case "thread/start", "thread/resume":
			resumed = m.Method == "thread/resume"
		case "turn/start":
			var text strings.Builder
			for _, in := range m.Params.Input {
				text.WriteString(in.Text + "\n")
			}
			out = append(out, codexTurn{Prompt: text.String(), Thread: m.Params.ThreadID, Resumed: resumed})
		}
	}
	return out
}
