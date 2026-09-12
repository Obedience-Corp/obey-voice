package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestRuntimeBuilderResumesOnlyTheRequestedChat(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	wasText, wasNoVoice := textMode, noVoice
	textMode, noVoice = true, true
	t.Cleanup(func() { textMode, noVoice = wasText, wasNoVoice })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"test","model":"test"}]}`)
			return
		}
		fmt.Fprintln(w, `{"model":"test","message":{"role":"assistant","content":"ready"},"done":true}`)
	}))
	defer srv.Close()
	data := fmt.Sprintf("brain_provider: ollama\nollama_host: %s\nollama_model: test\nvad_enabled: false\n", srv.URL)
	if err := os.WriteFile(filepath.Join(config.ConfigDir(), "config.yaml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := prompts.Seed(config.PromptsDir()); err != nil {
		t.Fatal(err)
	}
	writeAckTestPersona(t, "obey", "You are Obey.")
	initial := session.New("ollama", "test")
	if err := initial.Save([]brain.Turn{{Role: "user", Content: "Original history"}}); err != nil {
		t.Fatal(err)
	}
	build := conversationRuntimeBuilder(initial)
	for _, tc := range []struct {
		name, selected string
		wantResume     bool
	}{
		{name: "CLI resume", wantResume: true},
		{name: "New conversation starts fresh"},
		{name: "Continue reloads the saved session", selected: initial.ID, wantResume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rt, err := build(ctx, nil, tc.selected, "")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); rt.Cleanup() }()
			if tc.wantResume {
				if rt.SessionID != initial.ID || len(rt.Seed) != 1 || rt.Seed[0].Content != "Original history" {
					t.Fatalf("resumed id=%s seed=%v", rt.SessionID, rt.Seed)
				}
			} else if rt.SessionID == initial.ID || len(rt.Seed) != 0 || len(rt.Pipeline.Brain.History()) != 0 {
				t.Fatalf("new chat reused the initial session: id=%s seed=%v", rt.SessionID, rt.Seed)
			}
		})
	}
}
