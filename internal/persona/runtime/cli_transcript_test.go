package personaruntime

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
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

// installCLIFixture puts fake grok and cursor-agent executables on PATH. They
// re-run this test binary, log each request, and answer in the CLI's output
// shape with a reply that carries a speaker label. Both log to the same file.
func installCLIFixture(t *testing.T) string {
	t.Helper()
	binary := fixtureBinary(t)
	scripts := map[string]string{}
	for provider, name := range map[string]string{"grok": "grok", "cursor": "cursor-agent"} {
		scripts[name] = "#!/bin/sh\nOBEY_CLI_FIXTURE=" + provider + " exec '" + binary + "' -test.run='^TestCLITranscriptFixtureProcess$' -- \"$@\"\n"
	}
	dir := fixtureScriptDir(t, "cli", scripts)
	log := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OBEY_CLI_FIXTURE_LOG", log)
	return log
}

// cliRequest is one grok or cursor-agent fixture invocation: the prompt, the
// session it resumed and the one it answered in, and the persona its system
// prompt introduces, when the request carries one.
type cliRequest struct {
	Provider, Prompt, Format, Resume, Session, Persona string
	PID                                                int
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
	req := cliRequest{Provider: provider, PID: os.Getpid()}
	for i := 0; i < len(args); i++ {
		switch {
		case provider == "cursor" && args[i] == "--":
			req.Prompt, i = strings.Join(args[i+1:], " "), len(args)
		case i+1 >= len(args):
		case provider == "grok" && args[i] == "-p":
			req.Prompt, i = args[i+1], i+1
		case args[i] == "--resume":
			req.Resume, i = args[i+1], i+1
		case args[i] == "--output-format":
			req.Format, i = args[i+1], i+1
		case args[i] == "--system-prompt-override":
			if m := fixturePersonaName.FindStringSubmatch(args[i+1]); m != nil {
				req.Persona = m[1]
			}
			i++
		}
	}
	// Only the current request triggers a scripted reply; a conversation
	// record line may quote an earlier request.
	var current strings.Builder
	for line := range strings.Lines(req.Prompt) {
		if !strings.HasPrefix(line, "{") {
			current.WriteString(line)
		}
	}
	if m := fixturePersonaName.FindStringSubmatch(current.String()); m != nil && req.Persona == "" {
		req.Persona = m[1]
	}
	data, _ := os.ReadFile(log)
	n := bytes.Count(data, []byte("\n")) + 1
	req.Session = provider + "-fixture"
	if os.Getenv("OBEY_CLI_FIXTURE_SESSIONS") == "distinct" {
		req.Session = req.Resume
		if req.Session == "" {
			req.Session = fmt.Sprintf("%s-session-%d", provider, n)
		}
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		os.Exit(2)
	}
	line, _ := json.Marshal(req)
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	name := cmp.Or(req.Persona, "Mira")
	stall := strings.Contains(current.String(), claudeStall)
	text := fmt.Sprintf("%s: %s reply %d.", name, name, n)
	if stall {
		text = name + ": " + claudePartial
	}
	out := json.NewEncoder(os.Stdout)
	switch provider {
	case "grok":
		if req.Format == "json" {
			_ = out.Encode(map[string]any{"text": text, "sessionId": req.Session})
			break
		}
		_ = out.Encode(map[string]any{"type": "text", "data": text, "sessionId": req.Session})
		if stall {
			time.Sleep(time.Minute)
		}
	case "cursor":
		if stall {
			time.Sleep(time.Minute)
		}
		_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": text, "session_id": req.Session})
	}
	os.Exit(0)
}

func readCLIRequests(t *testing.T, log string) []cliRequest {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []cliRequest
	for line := range strings.Lines(string(data)) {
		var req cliRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req)
	}
	return out
}

// newCLIConversation is a conversation that starts on Mira, a persona on
// provider, with Ember on a fake Ollama server as the other participant.
func newCLIConversation(t *testing.T, provider string) *claudeConversation {
	t.Helper()
	c := &claudeConversation{t: t, log: installCLIFixture(t)}
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
