//go:build !integration && !windows

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// This process test exercises the real CLI, network server, persona selector,
// and persisted session without touching the user's install or model services.
func TestServeRestartProcess(t *testing.T) {
	if os.Getenv("OBEY_SERVE_RESTART_HELPER") == "1" {
		rootCmd.SetArgs([]string{"serve", "--bind", "127.0.0.1", "--port", os.Getenv("OBEY_SERVE_RESTART_PORT"), "--no-mdns", "--no-voice", "--remote-mic=false", "--banner-json"})
		if err := rootCmd.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"test-model","model":"test-model"}]}`)
		case "/speech/v1/models":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
		case "/api/chat":
			_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"Hello from the agent."},"done":false}`)
			_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	home := t.TempDir()
	dir := filepath.Join(home, ".obey", "agents", "voice", "obey-voice")
	for _, id := range []string{"obey", "jarvis"} {
		p := filepath.Join(dir, "personas", id)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf("schema: obey-voice.persona.v1\nid: %s\ndisplay_name: %s\nbrain:\n  provider: ollama\n  model: test-model\ntts:\n  provider: openai-speech\n  voice: Harper\nprompts:\n  persona: obey\n", id, id)
		if err := os.WriteFile(filepath.Join(p, "persona.yaml"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configData := fmt.Sprintf("active_persona: obey\nbrain_provider: ollama\nollama_host: %s\nollama_model: test-model\ntts_provider: openai-speech\nvoice_fallback_provider: disabled\nopenai_speech_runtime: external\nopenai_speech_url: %s/speech/v1\nopenai_speech_model: test-model\n", upstream.URL, upstream.URL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServeRestartProcess$")
	child.Env = append(os.Environ(), "HOME="+home, "OBEY_SERVE_RESTART_HELPER=1", "OBEY_SERVE_RESTART_PORT="+strconv.Itoa(port))
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	scanDone := make(chan struct{})
	go func() {
		<-scanDone
		done <- child.Wait()
	}()
	childExited := false
	t.Cleanup(func() {
		cancel()
		if childExited {
			return
		}
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Errorf("serve cleanup: %v", err)
			}
		case <-time.After(5 * time.Second):
		}
	})
	ready := make(chan map[string]any, 4)
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event["event"] == "ready" {
				ready <- event
			}
		}
	}()
	waitReady := func() map[string]any {
		t.Helper()
		select {
		case event := <-ready:
			return event
		case err := <-done:
			childExited = true
			t.Fatalf("serve exited: %v: %s", err, stderr.String())
		case <-ctx.Done():
			t.Fatal("serve did not become ready")
		}
		return nil
	}
	first := waitReady()
	token, _ := first["token"].(string)
	if token == "" {
		t.Fatal("ready banner has no token")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // isolated self-signed fixture
	dial := func() *websocket.Conn {
		t.Helper()
		ws, _, err := websocket.Dial(ctx, fmt.Sprintf("wss://127.0.0.1:%d/v1/stream?token=%s", port, token), &websocket.DialOptions{HTTPClient: client})
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	readUntil := func(ws *websocket.Conn, kind string) map[string]any {
		t.Helper()
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("read %s: %v", kind, err)
			}
			var event map[string]any
			if json.Unmarshal(raw, &event) == nil && event["type"] == kind {
				return event
			}
		}
	}
	ws := dial()
	_ = readUntil(ws, "session_snapshot")
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"set_persona","name":"jarvis","request_id":"test"}`)); err != nil {
		t.Fatal(err)
	}
	_ = readUntil(ws, "set_persona_ack")
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"type":"text_input","text":"Remember this"}`)); err != nil {
		t.Fatal(err)
	}
	_ = readUntil(ws, "response_ready")
	if err := requestServeControl(dir, port, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err == nil {
		// A queued event may precede the close; drain until it closes.
		for err == nil {
			_, _, err = ws.Read(ctx)
		}
		if ctx.Err() != nil {
			t.Fatal("old stream remained open after restart")
		}
	}
	second := waitReady()
	if second["token"] != first["token"] || second["pid"] != first["pid"] {
		t.Fatal("restart changed the process or pairing token")
	}
	ws = dial()
	snapshot := readUntil(ws, "session_snapshot")
	selection, _ := snapshot["selection"].(map[string]any)
	turns, _ := snapshot["transcript"].([]any)
	if selection["persona_id"] != "jarvis" || len(turns) < 2 {
		t.Fatalf("restart lost selection or transcript: selection=%v, turns=%d", selection["persona_id"], len(turns))
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
	if err := requestServeControl(dir, port, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		childExited = true
		if err != nil {
			t.Fatalf("stop: %v: %s", err, stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("serve did not stop")
	}
}
