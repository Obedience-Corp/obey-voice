package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type eventWriter struct{ messages chan map[string]any }

func (w *eventWriter) Write(b []byte) (int, error) {
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, err
	}
	select {
	case w.messages <- v:
	default:
	}
	return len(b), nil
}
func (w *eventWriter) until(t *testing.T, match func(map[string]any) bool) map[string]any {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case v := <-w.messages:
			if match(v) {
				return v
			}
		case <-timer.C:
			t.Fatal("timed out waiting for desktop state")
			return nil
		}
	}
}
func fakeInstall(t *testing.T) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"desktop-test","model":"desktop-test"}]}`)
			return
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		content := "Desktop reply preserved."
		if request["format"] != nil {
			content = `{"kind":"none","name":"","bare":false}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "desktop-test", "message": map[string]string{"role": "assistant", "content": content}, "done": true, "done_reason": "stop"})
	}))
	t.Cleanup(srv.Close)
	cfg := fmt.Sprintf("brain_provider: ollama\nollama_host: %s\nollama_model: desktop-test\nvad_enabled: false\ntalk_hotkey_enabled: false\nvoice_workers_disabled: true\nvoice_work_enabled: false\nvoice_tools_enabled: false\nskills_enabled: false\n", srv.URL)
	if err := os.WriteFile(filepath.Join(config.ConfigDir(), "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
}
func TestDesktopTextRoundTripPersistsAndResumes(t *testing.T) {
	fakeInstall(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &eventWriter{messages: make(chan map[string]any, 2048)}
	o := &output{enc: json.NewEncoder(writer)}
	h := &host{}
	defer h.close(o)
	value, err := h.handle(ctx, request{Method: "start", TextOnly: true, Silent: true, PersonaID: ":defaults"}, o)
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]any)["session_id"].(string)
	if _, err = h.handle(ctx, request{Method: "text", Text: "Hello desktop"}, o); err != nil {
		t.Fatal(err)
	}
	writer.until(t, func(v map[string]any) bool {
		if v["event"] != "state" {
			return false
		}
		data, _ := json.Marshal(v["data"])
		return strings.Contains(string(data), "Desktop reply preserved.")
	})
	if _, err = h.handle(ctx, request{Method: "stop"}, o); err != nil {
		t.Fatal(err)
	}
	saved, err := session.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Turns) < 2 || saved.Turns[0].Content != "Hello desktop" {
		t.Fatalf("persisted turns = %+v", saved.Turns)
	}
	if _, err = h.handle(ctx, request{Method: "start", TextOnly: true, Silent: true, SessionID: id, PersonaID: ":defaults"}, o); err != nil {
		t.Fatal(err)
	}
	writer.until(t, func(v map[string]any) bool {
		if v["event"] != "state" {
			return false
		}
		data, _ := json.Marshal(v["data"])
		return strings.Contains(string(data), "Hello desktop")
	})
	if _, err = h.handle(ctx, request{Method: "session_delete", SessionID: id}, o); err == nil {
		t.Fatal("active session deletion succeeded")
	}
}
func TestBridgeRejectsUnavailableActionsAndTraversal(t *testing.T) {
	fakeInstall(t)
	h := &host{}
	o := &output{enc: json.NewEncoder(&eventWriter{messages: make(chan map[string]any, 20)})}
	for _, r := range []request{{Method: "text", Text: "ignored"}, {Method: "meeting_stop"}, {Method: "session_delete", SessionID: "../../other"}, {Method: "start", SessionID: "../../other", TextOnly: true, Silent: true}} {
		if _, err := h.handle(t.Context(), r, o); err == nil {
			t.Fatalf("%+v succeeded", r)
		}
	}
}
func TestBridgeEOFAndMalformedJSON(t *testing.T) {
	writer := &eventWriter{messages: make(chan map[string]any, 20)}
	if err := run(t.Context(), strings.NewReader("not-json\n"), writer); err != nil {
		t.Fatal(err)
	}
	writer.until(t, func(v map[string]any) bool { return v["event"] == "error" })
}

func TestWorkerPolicyRejectsStaleEdits(t *testing.T) {
	fakeInstall(t)
	h := &host{}
	o := &output{enc: json.NewEncoder(&eventWriter{messages: make(chan map[string]any, 20)})}
	draft := config.DefaultVoiceWorkers()
	draft.Policy.ConcurrentWorkers = 2
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.handle(t.Context(), request{Method: "worker_config_save", Config: data, Revision: 0}, o); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handle(t.Context(), request{Method: "worker_config_save", Config: data, Revision: 0}, o); err == nil {
		t.Fatal("stale worker policy overwrote newer settings")
	}
	cfg, err := config.LoadRaw()
	if err != nil || cfg.VoiceWorkers.Policy.ConcurrentWorkers != 2 {
		t.Fatalf("saved policy missing: %v", err)
	}
}
