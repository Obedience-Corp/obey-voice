package personaruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// installCLIFixture puts a fake grok or cursor-agent executable on PATH. It
// re-runs this test binary, logs each request's arguments, and answers in the
// CLI's output shape with a reply that carries a speaker label.
func installCLIFixture(t *testing.T, provider string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := map[string]string{"grok": "grok", "cursor": "cursor-agent"}[provider]
	script := "#!/bin/sh\nexec '" + binary + "' -test.run='^TestCLITranscriptFixtureProcess$' -- \"$@\"\n"
	dir := fixtureScriptDir(t, name, map[string]string{name: script})
	log := filepath.Join(t.TempDir(), "requests.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_CLI_FIXTURE", provider)
	t.Setenv("OBEY_CLI_FIXTURE_LOG", log)
	return log
}

func TestCLITranscriptFixtureProcess(t *testing.T) {
	provider, log := os.Getenv("OBEY_CLI_FIXTURE"), os.Getenv("OBEY_CLI_FIXTURE_LOG")
	if provider == "" || log == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	request := strings.Join(args, " ")
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	line, _ := json.Marshal(request)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	data, _ := os.ReadFile(log)
	n := bytes.Count(data, []byte("\n"))
	stall := strings.Contains(request, claudeStall)
	text := fmt.Sprintf("Mira: Mira reply %d.", n)
	if stall {
		text = "Mira: " + claudePartial
	}
	out := json.NewEncoder(os.Stdout)
	switch provider {
	case "grok":
		_ = out.Encode(map[string]any{"type": "text", "data": text, "sessionId": "grok-fixture"})
		if stall {
			time.Sleep(time.Minute)
		}
	case "cursor":
		if stall {
			time.Sleep(time.Minute)
		}
		_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": text, "session_id": "cursor-fixture"})
	}
	os.Exit(0)
}

// newCLIConversation is a conversation that starts on Mira, a persona on
// provider, with Ember on a fake Ollama server as the other participant.
func newCLIConversation(t *testing.T, provider string) *claudeConversation {
	t.Helper()
	c := &claudeConversation{t: t, log: installCLIFixture(t, provider)}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/chat" {
			_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
			return
		}
		var req struct{ Messages []ollamaMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		c.mu.Lock()
		c.ollama = append(c.ollama, req.Messages)
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "message": map[string]string{"role": "assistant", "content": emberReply}, "done": true, "done_reason": "stop"})
	}))
	t.Cleanup(server.Close)
	c.catalog = persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for id, display := range map[string]string{"mira": "Mira", "ember": "Ember"} {
		profile := testProfile(id, display, id)
		profile.Brain.Model = ""
		if id == "mira" {
			profile.Brain.Provider = provider
		}
		writeTestProfile(t, c.catalog.Dir, profile)
		writeTestPrompt(t, promptDir, id, "You are {agent_name}.")
	}
	c.base = &config.Config{ActivePersona: "mira", BrainProvider: provider, GrokModel: "grok-4", CursorModel: "auto", OllamaModel: "fixture:latest", OllamaHost: server.URL,
		PromptsDir: promptDir, WorkDir: root, MaxHistory: 20, OllamaNumCtx: 8192}
	c.store = session.NewStore(filepath.Join(root, "sessions"))
	c.sess = session.New(provider, "")
	c.start("mira", nil, nil)
	return c
}

func TestGrokAndCursorConversationsKeepAnAttributedTranscript(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			c := newCLIConversation(t, provider)
			c.say("Note that the vault code is cobalt.")

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			turnCtx, interrupt := context.WithCancel(ctx)
			var once sync.Once
			if provider == "grok" {
				events.Subscribe(c.p.Events, func(events.ResponseDelta) { once.Do(interrupt) })
			} else {
				go func() {
					time.Sleep(300 * time.Millisecond)
					interrupt()
				}()
			}
			if err := c.p.RunTurnTextMode(turnCtx, "What was it? "+claudeStall); err == nil {
				t.Fatal("interrupted turn reported success")
			}
			interrupt()
			c.say("Say it again.")

			transcript := c.selector.Transcript()
			ready := c.responses()
			var mira []string
			for _, turn := range replies(transcript) {
				if turn.PersonaID != "mira" || turn.PersonaDisplayName != "Mira" || turn.ResponseID == "" || strings.HasPrefix(turn.Content, "Mira: ") {
					t.Fatalf("reply not attributed to Mira: %+v", turn)
				}
				mira = append(mira, turn.Content+"|"+turn.Disposition)
			}
			want := []string{"Mira reply 1.|", "Mira reply 3.|"}
			if provider == "grok" {
				want = []string{"Mira reply 1.|", claudePartial + "|interrupted", "Mira reply 3.|"}
			}
			if strings.Join(mira, ";") != strings.Join(want, ";") || len(ready) < 2 || ready[len(ready)-1].Envelope == nil {
				t.Fatalf("Mira's replies = %v, want %v (transcript %+v)", mira, want, transcript)
			}
			if users := len(transcript) - len(mira); users != 3 || transcript[2].Content != "What was it? "+claudeStall {
				t.Fatalf("user lines = %+v", transcript)
			}

			c.say("switch to Ember", "What is the vault code?")
			requests := c.ollamaRequests()
			if len(requests) != 1 {
				t.Fatalf("ollama requests = %d", len(requests))
			}
			var sent strings.Builder
			for _, m := range requests[0] {
				sent.WriteString(m.Role + ": " + m.Content + "\n")
			}
			if !strings.Contains(sent.String(), "Mira reply 3.") || !strings.Contains(sent.String(), "Note that the vault code is cobalt.") {
				t.Fatalf("Ember did not receive the attributed conversation:\n%s", sent.String())
			}
			saved := c.saved()
			final := c.selector.Transcript()
			if saved.SelectedPersonaID != "ember" || len(saved.Turns) != len(final) {
				t.Fatalf("saved session = %+v", saved)
			}
			for i := range final {
				if saved.Turns[i].Content != final[i].Content || saved.Turns[i].PersonaID != final[i].PersonaID || saved.Turns[i].Disposition != final[i].Disposition {
					t.Fatalf("saved turn %d = %+v, want %+v", i, saved.Turns[i], final[i])
				}
			}
		})
	}
}
